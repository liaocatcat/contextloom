package harness

import (
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ─────────────────────────────────────────────────────────────
// 轻量语义检索层（对标前沿 embedding 语义索引的零依赖实现）
//
// 度量 1：查询覆盖率 queryCoverage = |查询特征 ∩ 文本特征| / |查询特征|
//   - 对"文本很长但包含查询关键特征"宽容（模糊检索的目标）；
//   - 对空泛命中天然低分（交集少）；
//   - 内置中英编码术语同义词表：中文 bigram ↔ 英文词双向扩展，
//     解决"修登录超时"→ 含 timeout/login 的英文代码 的跨语言语义命中。
//
// 度量 2：稠密向量余弦（embedding.go 的特征哈希层）
//   - 查询与文本各自嵌入为 dim 维 L2 归一化向量（FNV-1a hashing trick）；
//   - 对特征分布/文本长度更鲁棒，与覆盖率互补；
//   - 二者在 semanticRerank 中以 0.5/0.5 权重融合参与最终评分。
//
// 零依赖、纯内存、惰性缓存（块特征向量/嵌入向量按需计算并缓存，
// 块内容更新时由 invalidateSemantic/invalidateEmbedding 同步失效）。
// ─────────────────────────────────────────────────────────────

// semanticCache 块特征向量缓存（blockID → features）。
var semanticCache sync.Map // map[string]map[string]float64

// invalidateSemantic 块内容变更时失效缓存（由 Index 写路径调用）。
func invalidateSemantic(blockID string) {
	semanticCache.Delete(blockID)
}

// ── 中英编码术语同义词表（中文 bigram → 英文词/短语）──
// 覆盖高频编码术语，用于跨语言语义扩展（查询与文本双向）。

var zhEnSynonyms = map[string][]string{
	"登录": {"login", "signin"}, "注销": {"logout", "signout"},
	"超时": {"timeout"}, "重试": {"retry"}, "退避": {"backoff"},
	"认证": {"auth", "authenticate"}, "鉴权": {"auth"}, "权限": {"permission", "perm"},
	"会话": {"session"}, "缓存": {"cache"}, "索引": {"index"},
	"搜索": {"search"}, "查询": {"query"}, "检索": {"search", "retrieve"},
	"错误": {"error", "err"}, "异常": {"exception", "panic"}, "警告": {"warn", "warning"},
	"配置": {"config", "cfg"}, "环境": {"env", "environment"}, "参数": {"arg", "param"},
	"文件": {"file"}, "目录": {"dir", "directory"}, "路径": {"path"},
	"删除": {"delete", "remove", "del"}, "创建": {"create"}, "更新": {"update"},
	"修改": {"modify", "change"}, "替换": {"replace"}, "追加": {"append"},
	"读取": {"read"}, "写入": {"write"}, "执行": {"exec", "run", "execute"},
	"命令": {"command", "cmd"}, "进程": {"process"}, "线程": {"thread"},
	"并发": {"concurrent", "parallel"}, "同步": {"sync"}, "异步": {"async"},
	"队列": {"queue"}, "任务": {"task"}, "目标": {"goal"},
	"计划": {"plan"}, "步骤": {"step"}, "结果": {"result"},
	"输出": {"output"}, "输入": {"input"}, "接口": {"api", "interface"},
	"函数": {"function", "func"}, "变量": {"variable", "var"}, "类型": {"type"},
	"模块": {"module"}, "依赖": {"dependency", "dep"}, "版本": {"version"},
	"端口": {"port"}, "地址": {"addr", "address"}, "链接": {"link", "url"},
	"日志": {"log"}, "监控": {"monitor", "metric"}, "指标": {"metric"},
	"部署": {"deploy"}, "发布": {"release"}, "合并": {"merge"},
	"分支": {"branch"}, "提交": {"commit"}, "推送": {"push"},
	"拉取": {"pull"}, "克隆": {"clone"}, "仓库": {"repo", "repository"},
	"代码": {"code"}, "注释": {"comment"}, "文档": {"doc", "docs"},
	"示例": {"example"}, "启动": {"start"}, "停止": {"stop"},
	"退出": {"exit", "quit"}, "重启": {"restart"}, "刷新": {"refresh"},
	"清理": {"clean", "cleanup"}, "压缩": {"compress", "compact"},
	"解压": {"extract"}, "上传": {"upload"}, "下载": {"download"},
	"安装": {"install"}, "卸载": {"uninstall"}, "注册": {"register"},
	"用户": {"user"}, "管理员": {"admin"}, "密码": {"password", "passwd"},
	"密钥": {"key", "secret"}, "令牌": {"token"}, "加密": {"encrypt", "cipher"},
	"解密": {"decrypt"}, "哈希": {"hash"}, "签名": {"sign"},
	"校验": {"verify", "validate"}, "解析": {"parse"}, "编码": {"encode"},
	"解码": {"decode"}, "字节": {"byte"}, "字符串": {"string"},
	"整数": {"int", "integer"}, "浮点": {"float"}, "布尔": {"bool"},
	"数组": {"array", "list"}, "映射": {"map"}, "结构": {"struct"},
	"实现": {"implement", "impl"}, "抽象": {"abstract"}, "继承": {"inherit"},
	"断言": {"assert"}, "覆盖": {"coverage", "cover"},
	"基准": {"benchmark"}, "分析": {"analyze", "analysis"}, "统计": {"stat", "stats"},
	"报告": {"report"}, "跟踪": {"trace"}, "告警": {"alert"},
	"通知": {"notify", "notification"}, "消息": {"message", "msg"}, "事件": {"event"},
	"回调": {"callback"}, "钩子": {"hook"}, "插件": {"plugin"},
	"扩展": {"extension", "ext"}, "组件": {"component"}, "服务": {"service"},
	"客户端": {"client"}, "服务器": {"server"}, "数据库": {"db", "database"},
	"事务": {"txn", "transaction"}, "死锁": {"deadlock"}, "限流": {"ratelimit", "throttle"},
	"熔断": {"circuitbreak"}, "降级": {"degrade", "downgrade"}, "回滚": {"rollback"},
	"快照": {"snapshot"}, "备份": {"backup"}, "恢复": {"restore", "recover"},
	"迁移": {"migrate", "migration"}, "升级": {"upgrade"}, "过期": {"expire", "expiry"},
	"失效": {"invalidate", "invalid"}, "初始化": {"init", "initialize"},
	"关闭": {"close", "shutdown"}, "打开": {"open"}, "截断": {"truncate"},
	"移动": {"move"}, "复制": {"copy"}, "符号": {"symlink", "symbol"},
	"所有者": {"owner"}, "大小": {"size"}, "文件名": {"filename"},
	"扩展名": {"ext"}, "工作目录": {"cwd", "workdir"}, "默认": {"default"},
	"强制": {"force"}, "递归": {"recursive"}, "通配": {"wildcard", "glob"},
	"正则": {"regex", "regexp"}, "匹配": {"match"}, "查找": {"find", "locate"},
	"定位": {"locate"}, "排序": {"sort"}, "过滤": {"filter"},
	"分组": {"group"}, "聚合": {"aggregate"}, "去重": {"dedup"},
	"拆分": {"split"}, "连接": {"join", "connect"}, "拼接": {"concat"},
	"格式化": {"format"}, "序列化": {"marshal", "serialize"}, "反序列化": {"unmarshal", "deserialize"},
	"健康": {"health"}, "心跳": {"heartbeat"}, "探活": {"ping", "probe"},
	"负载": {"load", "balance"}, "流量": {"traffic"}, "带宽": {"bandwidth"},
	"延迟": {"latency"}, "吞吐": {"throughput"}, "并发数": {"qps", "rps"},
	"内存": {"memory", "mem"}, "磁盘": {"disk"}, "网络": {"network", "net"},
	"请求": {"request", "req"}, "响应": {"response", "resp"}, "状态码": {"status", "code"},
	"标头": {"header"}, "正文": {"body"}, "表单": {"form"},
	"代理": {"proxy"},
	"网关": {"gateway"}, "路由": {"route", "router"}, "转发": {"forward"},
	"重定向": {"redirect"}, "跨域": {"cors"}, "安全": {"security", "secure"},
	"漏洞": {"vuln", "vulnerability"}, "攻击": {"attack", "exploit"},
	"注入": {"inject", "injection"}, "泄露": {"leak"}, "审计": {"audit"},
	"策略": {"policy"}, "规则": {"rule"}, "检查": {"check", "inspect"},
	"验证": {"verify", "validate"}, "测试": {"test"}, "构建": {"build"},
	"修复": {"fix"}, "缺陷": {"bug", "defect"}, "性能": {"perf", "performance"},
	"优化": {"optimize", "opt"}, "重构": {"refactor"}, "评审": {"review"},
	"质量": {"quality"}, "风格": {"style"}, "命名": {"naming"},
	"说明": {"readme", "description"},
}

// enZhSynonyms 英文词 → 中文 bigram 反向表（初始化时构建）。
var enZhSynonyms = func() map[string][]string {
	out := make(map[string][]string)
	for zh, ens := range zhEnSynonyms {
		for _, en := range ens {
			out[en] = append(out[en], zh)
		}
	}
	return out
}()

// tokenizeFeatures 提取文本语义特征：
//   - CJK 字符：连续中日韩文按 2-gram；
//   - ASCII 词：小写整词 + 3-gram 前缀；
//   - 同义词扩展：中文 bigram ↔ 英文词双向附加（跨语言语义命中）。
func tokenizeFeatures(text string) []string {
	var feats []string
	var cjkBuf []rune
	var asciiBuf []rune
	seen := make(map[string]bool)

	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			feats = append(feats, f)
		}
	}

	flushCJK := func() {
		for i := 0; i+1 < len(cjkBuf); i++ {
			bg := string(cjkBuf[i : i+2])
			add(bg)
			// 中文 bigram → 英文同义词扩展
			if ens, ok := zhEnSynonyms[bg]; ok {
				for _, en := range ens {
					add("w:" + en)
					if len(en) >= 3 {
						for j := 0; j+3 <= len(en); j++ {
							add("g:" + en[j:j+3])
						}
					}
				}
			}
		}
		cjkBuf = cjkBuf[:0]
	}
	flushASCII := func() {
		if len(asciiBuf) > 0 {
			word := strings.ToLower(string(asciiBuf))
			add("w:" + word)
			if len(word) >= 3 {
				for i := 0; i+3 <= len(word); i++ {
					add("g:" + word[i:i+3])
				}
			}
			// 英文词 → 中文 bigram 同义词扩展
			if zhs, ok := enZhSynonyms[word]; ok {
				for _, zh := range zhs {
					add(zh)
				}
			}
			asciiBuf = asciiBuf[:0]
		}
	}

	for _, r := range text {
		switch {
		case isCJK(r):
			flushASCII()
			cjkBuf = append(cjkBuf, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			asciiBuf = append(asciiBuf, unicode.ToLower(r))
		default:
			flushCJK()
			flushASCII()
		}
	}
	flushCJK()
	flushASCII()
	return feats
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一汉字
		(r >= 0x3400 && r <= 0x4DBF) || // 扩展 A
		(r >= 0x3040 && r <= 0x30FF) || // 日文假名
		(r >= 0xAC00 && r <= 0xD7AF) // 韩文
}

// featureVector 特征 → 权重（出现即 1，去重后权重均为 1；
// 覆盖率度量下无需词频加权，长文本不被稀释）。
func featureVector(feats []string) map[string]float64 {
	if len(feats) == 0 {
		return nil
	}
	m := make(map[string]float64, len(feats))
	for _, f := range feats {
		m[f] = 1
	}
	return m
}

// queryCoverage 查询覆盖率：|查询特征 ∩ 文本特征| / |查询特征|（0~1）。
// 对长文本宽容（只要包含查询关键特征即高分），空泛命中天然低分。
func queryCoverage(qset, tset map[string]float64) float64 {
	if len(qset) == 0 || len(tset) == 0 {
		return 0
	}
	intersect := 0
	for k := range qset {
		if _, ok := tset[k]; ok {
			intersect++
		}
	}
	return float64(intersect) / float64(len(qset))
}

// SemanticScore 计算查询与目标文本的语义相似度（0~1，查询覆盖率）。
// 导出 API 签名保持兼容（既有调用方/测试依赖覆盖率语义）。
func SemanticScore(query, text string) float64 {
	if query == "" || text == "" {
		return 0
	}
	return queryCoverage(featureVector(tokenizeFeatures(query)), featureVector(tokenizeFeatures(text)))
}

// SemanticCosine 计算查询与目标文本的稠密向量余弦相似度（-1~1）。
// 基于零依赖 hashing embedding（embedding.go），与 SemanticScore（覆盖率）
// 互补：对特征分布/文本长度更鲁棒，且支持跨语言（同义词双向扩展）。
// 内部共享包级 embedder（惰性初始化，默认 256 维）。
func SemanticCosine(query, text string) float64 {
	if query == "" || text == "" {
		return 0
	}
	emb := getEmbedder()
	return Cosine(emb.Embed(query), emb.Embed(text))
}

// blockFeatures 取块特征向量（惰性缓存，并发安全）。
func blockFeatures(id, content string) map[string]float64 {
	if v, ok := semanticCache.Load(id); ok {
		return v.(map[string]float64)
	}
	v := featureVector(tokenizeFeatures(content))
	semanticCache.Store(id, v)
	return v
}

// semanticRerank 对候选块做语义重排：
//   - 仅对前 limit 个候选计算语义分（性能控制，倒排已保证候选相关）；
//   - 混合评分（双度量融合）：
//     semantic  = 查询覆盖率 × 100（0~100，可解释、稀疏）
//     cosScore  = 稠密向量余弦 × 100（0~100，对分布/长度鲁棒）
//     final     = keyword + (semantic×0.5 + cosScore×0.5) × 0.5
//     上限 100，且不降低关键词分（关键词强命中不被语义稀释）；
//   - 余弦项使"中文查询 ↔ 英文代码块"（同义词扩展后特征重叠）
//     也能获得排名提升——补足纯覆盖率对跨语言场景的短板。
func semanticRerank(query string, blocks []types.ContextBlock, limit int) {
	if limit <= 0 || len(blocks) == 0 {
		return
	}
	if len(blocks) > limit {
		blocks = blocks[:limit]
	}
	qset := featureVector(tokenizeFeatures(query))
	if len(qset) == 0 {
		return
	}
	qvec := getEmbedder().Embed(query)
	for i := range blocks {
		tset := blockFeatures(blocks[i].ID, blocks[i].Content)
		sem := queryCoverage(qset, tset) * 100
		cos := Cosine(qvec, blockVector(blocks[i].ID, blocks[i].Content)) * 100
		kw := float64(blocks[i].Score)
		blend := kw + (sem*0.5+cos*0.5)*0.5
		if blend > kw {
			blocks[i].Score = types.Score(math.Min(100, blend))
		}
	}
}
