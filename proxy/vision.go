package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// 图片 vision fallback：请求里带图、而当前映射到的那一对「后端 + 模型」
// 看不了图时，整条请求改走配置好的 vision 后端，而不是按 INLINE_IMAGE_POLICY
// 把图丢掉。对应原 Python 桥的 IMAGE_FALLBACK_MODE / _BACKEND / _MODEL。
//
// 与 INLINE_IMAGE_POLICY 的优先级（两边不会重复处理同一张图）：
//   - preserve：用户显式要求「照发原模型」，fallback 完全不介入，
//     图片按原样转发给当前模型（哪怕它是纯文本模型）。
//   - omit / omit_inline / auto：先尝试 fallback。fallback 真正接管时，
//     本次请求的图片原样转发，绝不走丢弃/占位分支；fallback 不接管
//     （未配置、后端不可用、目标模型看不了图、mode=off）时，
//     才按 INLINE_IMAGE_POLICY 的既有语义丢弃与补占位。
const (
	imageFallbackAuto = "auto"
	imageFallbackOff  = "off"
)

// ImageFallbackOptions 是 vision fallback 的显式配置入口。
// local 包从 config.json 的 image_fallback 翻译成它，容器的 serve
// 则从 IMAGE_FALLBACK_* 环境变量构造。
type ImageFallbackOptions struct {
	Mode    string
	Backend string
	Model   string
}

// imageFallback 是归一化之后的配置。Backend/Model 为空表示未配置：
// 此时 mode=auto 也不会接管任何请求，图片按 INLINE_IMAGE_POLICY 处理，
// 与补齐 fallback 之前的行为完全一致。
type imageFallback struct {
	Mode    string
	Backend string
	Model   string
}

// normalizeImageFallbackMode 把各种写法收敛到 auto / off。
// 第二个返回值报告输入是否可识别；不可识别的值按 off 处理并告警，
// 这样拼错变量名时只会「不生效」，不会把图误发给别的后端。
func normalizeImageFallbackMode(raw string) (mode string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "on", "always":
		return imageFallbackAuto, true
	case "off", "never", "none", "disabled", "disable":
		return imageFallbackOff, true
	case "preserve", "keep", "passthrough":
		// 原 Python 配置里 preserve 的语义是「照发原模型」，
		// 与 INLINE_IMAGE_POLICY=preserve 一致：不做任何转投。
		return imageFallbackOff, true
	default:
		return imageFallbackOff, false
	}
}

// isAuto 报告 fallback 是否允许接管图片请求。零值（未配置）视为 auto，
// 但 Backend/Model 为空时依然不会接管，因此不会改变老配置的行为。
func (f imageFallback) isAuto() bool {
	switch strings.ToLower(strings.TrimSpace(f.Mode)) {
	case "", imageFallbackAuto:
		return true
	}
	return false
}

// 已知能读图的模型家族。名字落在这些家族里才认为当前模型有视觉出口；
// 其余一律按纯文本处理——未知名字（deepseek-flash 之类）才是常态，
// 把图发给它只会被上游 400 拒绝。名字不在列表里但确实能读图时，
// 用 image_fallback.mode = "off" + INLINE_IMAGE_POLICY = "preserve" 覆盖。
var (
	visionModelPrefix = regexp.MustCompile(`(?i)^(gpt-4o|gpt-4\.1|gpt-4\.5|gpt-4-turbo|gpt-4-vision|gpt-5|chatgpt-4o|o1|o3|o4|claude-|gemini|glm-4v|glm-4\.1v|glm-4\.5v|internvl|llava|pixtral|minicpm-v|deepseek-vl)`)
	visionModelToken  = regexp.MustCompile(`(?i)(vision|-vl|_vl|vlm)`)
)

// modelLooksVisionCapable 按模型名判断视觉能力。只看名字，不打电话给上游。
func modelLooksVisionCapable(model string) bool {
	name := strings.TrimSpace(model)
	if name == "" {
		return false
	}
	return visionModelPrefix.MatchString(name) || visionModelToken.MatchString(name)
}

// backendSeesImages 报告这对后端 + 模型能不能看到图。
//
// upstream_mode=anthropic 的上游一律不参与 fallback：显式选了这种模式的请求
// 保持在原后端，图片交给 INLINE_IMAGE_POLICY 处理。注意 M1 并没有实现 Anthropic
// 直通——这种配置只会打一条「not supported」告警，callUpstream 仍走
// /chat/completions（见 proxy/config.go 与 callUpstream）——所以这条分支的实际
// 效果是「抑制 fallback」而非「图片原生直通」。要启用 fallback，请把该后端配成
// upstream_mode=openai（默认值）。
//
// 其余上游按模型名推断视觉能力。
func backendSeesImages(b *backend, model string) bool {
	if b != nil && normalizeUpstreamMode(b.rawMode) == "anthropic" {
		return true
	}
	return modelLooksVisionCapable(model)
}

// requestCarriesImages 报告请求里是否有 bridge 真能转发的图片块。
// tool_result 的 content 会被拍平成文本，所以嵌在里面的图不算——
// 那种情况下转投到 vision 后端也没有视觉出口。
func requestCarriesImages(req *anthropicRequest) bool {
	for _, msg := range req.Messages {
		if msg.Role == "assistant" {
			continue
		}
		_, blocks, isText := decodeBlocks(msg.Content)
		if isText {
			continue
		}
		for _, block := range blocks {
			if block.Type != "image" && block.Type != "image_url" {
				continue
			}
			if openAIImageURL(block) != "" {
				return true
			}
		}
	}
	return false
}

// imageFallbackResolution 是一次 fallback 判定的结果。
type imageFallbackResolution struct {
	backend *backend
	model   string
	// reason 说明 fallback 为什么不能接管；能接管时为空。
	reason string
}

// resolveImageFallback 检查配置本身：mode、后端是否可用、模型是否读图。
func (c *config) resolveImageFallback() imageFallbackResolution {
	if !c.ImageFallback.isAuto() {
		return imageFallbackResolution{reason: "mode=" + orNone(c.ImageFallback.Mode)}
	}
	if strings.TrimSpace(c.ImageFallback.Backend) == "" {
		return imageFallbackResolution{reason: "fallback backend is not set"}
	}
	b, ok := c.backends[c.ImageFallback.Backend]
	if !ok {
		return imageFallbackResolution{reason: fmt.Sprintf("unknown fallback backend %q", c.ImageFallback.Backend)}
	}
	if !c.backendUsable(b) {
		return imageFallbackResolution{reason: fmt.Sprintf("backend %q has no API key or base URL", b.name)}
	}
	if strings.TrimSpace(c.ImageFallback.Model) == "" {
		return imageFallbackResolution{reason: "fallback model is not set"}
	}
	if !backendSeesImages(b, c.ImageFallback.Model) {
		// 目标自己也读不了图：转投只会把图从一个纯文本模型搬到另一个，
		// 既不发图也不丢图的「双重处理」就是从这里杜绝的。
		return imageFallbackResolution{reason: fmt.Sprintf("fallback model %s cannot read images", c.ImageFallback.Model)}
	}
	return imageFallbackResolution{backend: b, model: strings.TrimSpace(c.ImageFallback.Model)}
}

// imageFallbackFor 决定这条请求是否要整体改走 vision 后端。
// 返回 false 时调用方保持 INLINE_IMAGE_POLICY 的既有行为。
func (c *config) imageFallbackFor(req *anthropicRequest, b *backend, backendModel string) (imageFallbackResolution, bool) {
	if !requestCarriesImages(req) {
		return imageFallbackResolution{}, false
	}
	// preserve 是「我知道自己在做什么，照发原模型」的显式选择。
	if strings.EqualFold(strings.TrimSpace(c.InlineImagePolicy), "preserve") {
		return imageFallbackResolution{}, false
	}
	if backendSeesImages(b, backendModel) {
		return imageFallbackResolution{}, false
	}
	fb := c.resolveImageFallback()
	if fb.backend == nil {
		return imageFallbackResolution{}, false
	}
	if fb.backend.name == b.name && fb.model == backendModel {
		return imageFallbackResolution{}, false
	}
	return fb, true
}
