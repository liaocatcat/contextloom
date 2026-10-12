package scenes

import "strings"

// IntentClassifier 意图分类器（模型层，注入式）。给定场景的 intent_prompt 与
// 用户输入，判断该场景是否命中。由调用方提供实现（如用模型网关包装）。
// 为 nil 时模型层关闭，仅走规则层。
type IntentClassifier func(intentPrompt, userInput string) (bool, error)

// Match 两级漏斗意图解析入口：
//
//  1. 规则层：遍历 catalog，场景 keywords 与用户输入做大小写不敏感的包含匹配，
//     命中即返回（确定性优先，不消耗 token）；
//  2. 模型层：规则层未命中且 classifier 非 nil 时，逐场景用 intent_prompt 判断；
//  3. 全部未命中：返回 nil——调用方兜底退回 chat 普通模式，绝不阻塞。
//
// 注意：模型层单场景判断出错时跳过该场景继续尝试其余场景（绝不向上抛错），
// 保证"意图解析永不失败、永不阻塞用户输入"。
func Match(catalog []Scene, input string, classifier IntentClassifier) *Scene {
	// ① 规则层：关键词大小写不敏感包含匹配
	lowInput := strings.ToLower(input)
	for i := range catalog {
		for _, kw := range catalog[i].Keywords {
			if kw == "" {
				continue
			}
			if strings.Contains(lowInput, strings.ToLower(kw)) {
				s := catalog[i]
				return &s
			}
		}
	}

	// ② 模型层：逐场景调用注入的分类器
	if classifier != nil {
		for i := range catalog {
			if strings.TrimSpace(catalog[i].IntentPrompt) == "" {
				continue
			}
			hit, err := classifier(catalog[i].IntentPrompt, input)
			if err != nil || !hit {
				continue
			}
			s := catalog[i]
			return &s
		}
	}

	// ③ 全未命中 → nil（兜底退回 chat 普通模式）
	return nil
}
