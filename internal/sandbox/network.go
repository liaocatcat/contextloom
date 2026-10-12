// 网络隔离：代理级拒绝（对标前沿容器网络隔离 --network=none 的零依赖落地）。
//
// 原理：沙箱会话内命令的 HTTP(S)/ALL 代理环境变量（大小写双份）强制指向
// 死端口 http://127.0.0.1:9（端口 9 = discard，任何连接立即失败），并删除
// NO_PROXY/no_proxy（防止本地豁免绕过）。遵守代理规范的程序（curl/wget/
// python requests/go http 等）全部外联失败，仅本机回环可通——与容器
// --network=none 在"不可外联"语义上等价，零内核依赖、全平台可交叉编译。
package sandbox

import "strings"

// proxyVars 需要指向死端口的代理环境变量（大小写双份）。
var proxyVars = []string{
	"HTTP_PROXY", "http_proxy",
	"HTTPS_PROXY", "https_proxy",
	"ALL_PROXY", "all_proxy",
}

// noProxyVars 需要被删除的 NO_PROXY 变量（大小写双份，防豁免绕过）。
var noProxyVars = []string{"NO_PROXY", "no_proxy"}

// denyProxyAddr 死端口代理地址：端口 9 = discard，连接立即失败。
const denyProxyAddr = "http://127.0.0.1:9"

// netDenyMark 沙箱网络拒绝标记（环境变量）。
const netDenyMark = "CONTEXTLOOM_SANDBOX_NET=deny"

// NetDenyEnv 返回 env 的副本并施加"网络拒绝"改造：
//   - 6 个代理变量（HTTP_PROXY/http_proxy/HTTPS_PROXY/https_proxy/
//     ALL_PROXY/all_proxy）全部设置为 http://127.0.0.1:9（死端口，
//     连接立即失败；宿主环境缺失的代理变量也会补齐，保证沙箱内
//     任何遵守代理规范的程序都被拒绝——网络隔离不依赖宿主环境）；
//   - 删除 NO_PROXY/no_proxy（防本地豁免绕过隔离）；
//   - 追加 CONTEXTLOOM_SANDBOX_NET=deny 标记（已存在则覆盖）。
//
// 幂等：对结果再次调用无变化。不修改原切片。
func NetDenyEnv(env []string) []string {
	seenProxy := make(map[string]bool, len(proxyVars))
	out := make([]string, 0, len(env)+len(proxyVars)+1)
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		switch {
		case isProxyVar(name):
			// 代理变量统一指向死端口（后置覆盖 envDenylist 等任何既有值）
			out = append(out, name+"="+denyProxyAddr)
			seenProxy[name] = true
		case isNoProxyVar(name), name == "CONTEXTLOOM_SANDBOX_NET":
			// NO_PROXY/no_proxy 删除（防豁免）；旧的 deny 标记删除后统一重加
		default:
			out = append(out, e)
		}
	}
	// 补齐宿主环境缺失的代理变量：网络隔离必须自足，不能依赖宿主代理配置
	for _, p := range proxyVars {
		if !seenProxy[p] {
			out = append(out, p+"="+denyProxyAddr)
		}
	}
	out = append(out, netDenyMark)
	return out
}

// isProxyVar 判断是否为需指向死端口的代理变量名。
func isProxyVar(name string) bool {
	for _, p := range proxyVars {
		if name == p {
			return true
		}
	}
	return false
}

// isNoProxyVar 判断是否为需删除的 NO_PROXY 变量名。
func isNoProxyVar(name string) bool {
	for _, p := range noProxyVars {
		if name == p {
			return true
		}
	}
	return false
}
