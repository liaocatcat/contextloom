// Command contextloom-gui 是 ContextLoom 的图形界面启动器（独立可执行文件，双击即启）。
//
// 启动流程：
//  1. 定位 contextloom 主程序（同目录优先 → CONTEXTLOOM_BIN 环境变量 → PATH）
//  2. 若 backend（contextloom --serve）未运行则自动拉起（headless、端口自动选择、
//     项目目录 = 启动 contextloom-gui 时的工作目录）
//  3. 解析 backend 输出的 GUI_ADDR 获取端口与 token
//  4. 启动本地静态服务（go:embed 内嵌 web/ 前端），/api/ws 反向代理到 backend
//     （token 只存在 contextloom-gui 进程内，不暴露给浏览器）
//  5. 打开系统默认浏览器（Wails/WebView 壳接入点预留，见 GUI实现设计文档.md）
//
// contextloom 的 TUI 启动方式不受影响：backend 是独立 headless 进程，存储为纯
// 文件无锁，与 TUI 实例天然并行（多实例并发支持）。
package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing/fstest"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/envclean"
	"github.com/contextloom/contextloom-agent/pkg/version"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:web
var webFS embed.FS

// 命令行参数（包级：runDesktop/stopOwnBackend 等函数需要访问）。
var (
	flagBrowser = flag.Bool("browser", false, "使用系统浏览器打开（默认使用桌面窗口）")
	flagStop    = flag.Bool("stop", false, "停止后台 contextloom 服务并退出")
	flagPort    = flag.Int("port", 0, "浏览器模式自身端口（0=自动选择）")
	flagBackend = flag.String("backend", "", "直接连接已有 backend 地址（跳过拉起）")
	flagVersion = flag.Bool("version", false, "显示版本号并退出")
)

// state 记录 backend 地址与窗口尺寸，供下次启动复用（秒开 + 窗口记忆）。
type state struct {
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
	Dir  string `json:"dir"`
	TS   int64  `json:"ts"`
	WinW int    `json:"winW,omitempty"`
	WinH int    `json:"winH,omitempty"`
}

func statePath() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "contextloom-gui", "state.json")
}

func loadState() (*state, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(s *state) error {
	if err := os.MkdirAll(filepath.Dir(statePath()), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return os.WriteFile(statePath(), b, 0o600)
}

func clearState() { _ = os.Remove(statePath()) }

// findContextLoomBin 定位 contextloom 主程序：同目录 → CONTEXTLOOM_BIN → PATH。
func findContextLoomBin() (string, error) {
	// 1. 同目录（contextloom-gui 与 contextloom 一起部署）
	//
	// 注意：必须优先用 os.Executable() 而非 os.Args[0]——macOS .app 场景
	// （LaunchServices/Finder 启动）argv[0] 可能是相对名或短名，filepath.Abs
	// 基于启动 cwd 解析后指向不存在路径，同目录查找会失败并错误回退 PATH
	// （分发到无 contextloom 的机器则 backend 找不到）。os.Executable() 返回
	// 真实可执行文件路径（含 .app/Contents/MacOS/），保证 .app 自包含。
	exePath := ""
	if exe, err := os.Executable(); err == nil {
		exePath = exe
	}
	if exePath == "" {
		exePath = os.Args[0]
	}
	if p, err := filepath.Abs(exePath); err == nil {
		exePath = p
	}
	if fi, err := os.Stat(exePath); err == nil && !fi.IsDir() {
		if bin, ok := findContextLoomInDir(filepath.Dir(exePath)); ok {
			return bin, nil
		}
	}
	// 2. 环境变量
	if v := os.Getenv("CONTEXTLOOM_BIN"); v != "" {
		if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
			return v, nil
		}
	}
	// 3. PATH：先精确名，再带平台后缀的产物名（如 contextloom-linux-amd64）
	for _, name := range []string{"contextloom", "contextloom.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if bin, ok := findContextLoomPlatformBinInDir(dir); ok {
			return bin, nil
		}
	}
	return "", fmt.Errorf("未找到 contextloom 主程序（请在 contextloom-gui 同目录放置 contextloom，或设置 CONTEXTLOOM_BIN）")
}

// findGUIConfigPath 定位 GUI 侧配置文件（供 GUI 自身与 backend 统一使用）。
// 返回的路径一定存在；全部不存在时返回 ""（调用方走内置默认配置）。
func findGUIConfigPath() string {
	exe, _ := os.Executable()
	return findGUIConfigPathFromExe(exe)
}

// findGUIConfigPathFromExe 纯函数：由可执行文件路径推导配置路径（便于单测）。
// 完整查找链：真实本体目录（解析符号链接）→ 原始 exe 目录/.app 同级 →
// CWD config.yaml → ""（内置默认）。
// 历史缺陷：经 PATH 符号链接启动时 os.Executable() 返回链接路径，导致
// 真实本体目录的 config.yaml 被无视、静默回退 CWD——先解析符号链接探测
// 本体目录，保证 GUI 与 backend（contextloom --serve）读取同一份配置。
func findGUIConfigPathFromExe(exe string) string {
	if exe != "" {
		if real, err := filepath.EvalSymlinks(exe); err == nil && real != exe {
			if p := guiConfigPathFromExe(real); p != "" {
				return p
			}
		}
		if p := guiConfigPathFromExe(exe); p != "" {
			return p
		}
	}
	if p := "config.yaml"; fileExists(p) {
		abs, _ := filepath.Abs(p)
		return abs
	}
	return ""
}

// fileExists 判断路径是否存在（main 包本地辅助；config 包同名函数未导出）。
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// guiConfigPathFromExe 纯函数：由可执行文件路径推导配置路径（便于单测）。
func guiConfigPathFromExe(exe string) string {
	exeDir := filepath.Dir(exe)
	// 1. exe 同目录
	if p := filepath.Join(exeDir, "config.yaml"); fileExists(p) {
		return p
	}
	// 2. .app 打包场景：exe = .../ContextLoom.app/Contents/MacOS/contextloom-gui，
	//    上溯 3 级到 ContextLoom.app，再取父目录（应用包同级）。
	if strings.Contains(exe, string(filepath.Separator)+"Contents"+string(filepath.Separator)+"MacOS"+string(filepath.Separator)) {
		appParent := filepath.Dir(filepath.Dir(filepath.Dir(exeDir)))
		if p := filepath.Join(appParent, "config.yaml"); fileExists(p) {
			return p
		}
	}
	return ""
}

// findContextLoomInDir 在指定目录查找 contextloom 主程序（同目录部署场景）。
// 独立纯函数便于单测（findContextLoomBin 内部依赖 os.Executable 无法注入）。
//
// 名称匹配规则：
//  1. 精确名优先：contextloom（unix）/ contextloom.exe（Windows）
//  2. 兼容交叉编译产物名：contextloom-<GOOS>-<GOARCH>[.exe]，如
//     contextloom-linux-amd64、contextloom-windows-arm64.exe、contextloom-darwin-arm64。
//     依据：Makefile/文档的跨平台打包命令产出此类名称，用户把 TUI 产物放到
//     GUI 同目录分发时不应因文件名带后缀而找不到 backend。
//     仅接受合法 <GOOS>-<GOARCH> 后缀，避免误命中 contextloom-gui 等无关文件。
func findContextLoomInDir(dir string) (string, bool) {
	candidates := []string{"contextloom", "contextloom.exe"}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	// 带平台后缀的产物：contextloom-<GOOS>-<GOARCH>[.exe]
	// 同目录并存多平台产物时优先当前平台，其余平台兜底——保证"包里带了 backend 就能拉起"。
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	plat := runtime.GOOS + "-" + runtime.GOARCH
	suffixes := []string{"", ".exe"}
	fallback := ""
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		for _, ext := range suffixes {
			if !strings.HasSuffix(name, ext) {
				continue
			}
			base := strings.TrimSuffix(name, ext)
			if _, _, ok := splitPlatformSuffix(base); ok {
				if strings.Contains(base, plat) {
					return filepath.Join(dir, name), true
				}
				if fallback == "" {
					fallback = filepath.Join(dir, name)
				}
			}
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// findContextLoomPlatformBinInDir 在 PATH 目录中查找带平台后缀的 TUI 产物
// （contextloom-<GOOS>-<GOARCH>，不带 .exe——PATH 场景下精确名已由 LookPath 处理）。
func findContextLoomPlatformBinInDir(dir string) (string, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, ent := range ents {
		if ent.IsDir() || strings.Contains(ent.Name(), ".") {
			continue
		}
		if _, _, ok := splitPlatformSuffix(ent.Name()); ok {
			return filepath.Join(dir, ent.Name()), true
		}
	}
	return "", false
}

// splitPlatformSuffix 判断 base 是否为 contextloom-<GOOS>-<GOARCH> 形式。
// <GOOS>/<GOARCH> 必须是 Go 已知的合法平台组合（go tool dist list 子集），
// 防止误命中 contextloom-gui、contextloom-agent-config 之类的无关文件。
func splitPlatformSuffix(base string) (string, string, bool) {
	const prefix = "contextloom-"
	if !strings.HasPrefix(base, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(base, prefix)
	dash := strings.LastIndex(rest, "-")
	if dash <= 0 || dash == len(rest)-1 {
		return "", "", false
	}
	goos, goarch := rest[:dash], rest[dash+1:]
	if !validPlatform(goos, goarch) {
		return "", "", false
	}
	return goos, goarch, true
}

// validPlatform 白名单校验：Makefile 与文档打包命令覆盖的 GOOS/GOARCH 组合。
func validPlatform(goos, goarch string) bool {
	switch goarch {
	case "amd64", "arm64":
		switch goos {
		case "linux", "darwin", "windows":
			return true
		}
	}
	return false
}

// probeBackend 探测 backend 是否存活。
//
// 注意：addr 是完整地址（含 /?token=...），【不能】直接拼接 "/healthz"——
// 那会变成 "?token=xxx/healthz"（healthz 路径丢失、token 值被污染），
// backend 返回 403 → 误判为"不存活" → stop() 不杀旧 backend →
// 切换项目/退出时进程无限堆积（历史 bug：switch 后旧 backend 永不退出）。
// 正确做法：解析出 base（scheme+host），再挂 /healthz。
func probeBackend(addr string) bool {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	u.Path = "/healthz"
	u.RawQuery = "" // 清理 token（healthz 无需鉴权）
	u.Fragment = ""
	cli := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := cli.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// shouldReuseBackend 判断 GUI 启动时是否复用 state.json 记录的运行中
// backend。复用条件（缺一不可）：
//  1. 记录地址非空、探活成功（进程确实存活）；
//  2. 【目录一致性】记录的 backend 项目目录与当前启动项目目录一致——
//     否则复用到其他项目残留的 backend，会把会话/文件树/终端等全部
//     数据写到错误项目（跨项目会话串联根因：上次 GUI 崩溃/强杀残留的
//     backend 被新 GUI 无条件复用，用户在任何项目新建会话都串到旧项目）。
//
// dir 为空/历史 state 无目录字段时同样不复用（保守：宁启动新 backend，
// 也不冒串项目风险）。
func shouldReuseBackend(st *state, dir string, probe func(addr string) bool) bool {
	if st == nil || st.Addr == "" || st.Dir == "" || st.Dir != dir {
		return false
	}
	return probe(st.Addr)
}

// parseGUIAddrLine 解析 backend stdout 中的 "GUI_ADDR=<url>" 行。
func parseGUIAddrLine(line string) (string, bool) {
	const prefix = "GUI_ADDR="
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	addr := strings.TrimPrefix(line, prefix)
	if addr == "" {
		return "", false
	}
	return addr, true
}

// startBackend 拉起 contextloom --serve（headless），解析 GUI_ADDR 返回地址。
// 诊断友好：捕获 stderr，进程退出/超时时输出其内容辅助定位（如二进制过旧
// 不含 --serve、runtime 初始化失败等）。正常路径实测 <1s。
//
// 返回的 done channel 在 backend 进程退出时关闭（由内部 Wait goroutine
// 回收退出状态后关闭）：调用方 stop() 靠它等待优雅退出、超时强杀，同时
// 保证退出状态必被回收（防僵尸进程——历史 bug：成功路径从不 Wait，
// 每次切换项目/退出都残留一个 <defunct>）。
func startBackend(bin, dir string) (string, int, <-chan struct{}, error) {
	// 配置跟随 contextloom-gui 可执行文件同目录/.app 同级/CWD（见 findGUIConfigPath）：
	// 显式传 --config，保证 backend 与 GUI 读同一份配置（否则 backend 用自己的
	// os.Executable() 定位——.app 场景指向 Contents/MacOS/，读不到用户放在
	// GUI 同目录的 config.yaml）。
	args := []string{"--serve", "--serve-port=0"}
	if cfgPath := findGUIConfigPath(); cfgPath != "" {
		args = append(args, "--config", cfgPath)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	// Windows 下 contextloom-gui 为 GUI 子系统（-H windowsgui 构建）自身无
	// 控制台：若不设置 CREATE_NO_WINDOW，console 子系统子进程（contextloom
	// --serve）会被 Windows 新建控制台窗口——启动 GUI 即弹黑窗。设置后
	// backend 静默后台运行，输出仍走 StdoutPipe/Stderr 重定向不受影响。
	cmd.SysProcAttr = hiddenProcAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", 0, nil, fmt.Errorf("启动 contextloom 失败: %w", err)
	}
	// 进程回收 goroutine：backend 退出时回收退出状态并关闭 done。
	// 在成功路径同样生效（失败路径的 Wait 见下方 scanner goroutine）。
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	// 扫描 stdout 中的 GUI_ADDR=... 行（格式固定，见 cmd/contextloom runGUIServer）
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	stopScan := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			select {
			case <-stopScan:
				return // 已拿到地址：停止扫描，避免 goroutine 泄漏
			default:
			}
			if addr, ok := parseGUIAddrLine(sc.Text()); ok {
				addrCh <- addr
				return
			}
		}
		if err := sc.Err(); err != nil {
			errCh <- err
			return
		}
		// stdout EOF：先 Wait 确保进程退出、stderr 收集完成（否则 stderr 缓冲
		// 可能未写完，错误诊断丢失内容），再拼错误信息。Wait 由上面的
		// goroutine 负责（此处不重复 Wait，避免双 Wait 竞争）。
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		errCh <- fmt.Errorf("contextloom 未输出 GUI_ADDR 即退出（stderr: %s）",
			strings.TrimSpace(stderr.String()))
	}()
	const readyTimeout = 15 * time.Second
	select {
	case addr := <-addrCh:
		close(stopScan)
		return addr, cmd.Process.Pid, done, nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		<-done
		return "", 0, done, err
	case <-time.After(readyTimeout):
		_ = cmd.Process.Kill()
		<-done
		return "", 0, done, fmt.Errorf("等待 contextloom 服务就绪超时（%v，stderr: %s）",
			readyTimeout, strings.TrimSpace(stderr.String()))
	}
}

// wsProxy 构建到 backend 的 WebSocket 反向代理（注入 token）。
func wsProxy(backendAddr, token string) *httputil.ReverseProxy {
	u, err := url.Parse(backendAddr)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		// 防御：backend 地址异常时给出明确错误（而非静默产生空 scheme 代理）
		return &httputil.ReverseProxy{
			Director: func(r *http.Request) {
				r.URL.Scheme = ""
				r.URL.Host = ""
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				http.Error(w, "backend 地址无效: "+backendAddr, http.StatusBadGateway)
			},
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	// 显式设置目标 scheme/host：不依赖 NewSingleHostReverseProxy 内部
	// Director 实现，杜绝 "unsupported protocol scheme"（空 scheme）错误。
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = u.Scheme
		r.URL.Host = u.Host
		// 重写路径到 /ws 并注入 token（浏览器只接触 contextloom-gui，token 不泄露）
		r.URL.Path = "/ws"
		q := r.URL.Query()
		q.Set("token", token)
		r.URL.RawQuery = q.Encode()
		r.Host = u.Host
	}
	// 静默 backend 连接失败/关闭等常规错误：前端有自动重连，
	// 避免每 2 秒刷一条 "proxy error" 日志。
	proxy.ErrorLog = log.New(os.Stderr, "[ws-proxy] ", 0)
	return proxy
}

// backendMgr 管理 contextloom backend 进程（拉起/停止/切换项目目录）。
//
// 需求：GUI 应能打开其他目录，且 backend 识别到的项目目录以打开目录为根
// （配置文件除外——config.yaml 仍跟随 contextloom 可执行文件目录，由 contextloom
// 自身的 defaultConfigPath 保证，backend 的 cwd 只影响项目根与存储位置）。
// projectsFile 项目列表持久化文件（GUI 自身配置，不属于任何项目；
// 存用户配置目录，切换/重启 GUI 后仍保留）。
func projectsFile() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	return filepath.Join(d, "contextloom-gui", "projects.json")
}

// loadProjectsFrom 读取持久化项目列表；文件不存在/损坏返回空列表（不报错）。
func loadProjectsFrom(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v struct {
		Projects []string `json:"projects"`
	}
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	return v.Projects
}

func saveProjectsTo(path string, projects []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"projects": projects}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ownedBackend 记录一个已拉起 backend 的完整状态（backendMgr.backends 池元素）。
// 切换项目【不杀旧 backend】：每个项目一个常驻进程，正在进行的对话/在途流
// 随进程保留，切回时立即可见（"切换项目右侧会话不关闭"的根基）。
type ownedBackend struct {
	addr  string // 完整 backend 地址（含 token）
	base  string // 无 token 的 base URL（http://127.0.0.1:port）
	token string
	pid   int
	own   bool            // 本 GUI 进程拉起的（退出/移除时终止）；复用的不杀
	done  <-chan struct{} // 进程退出信号（stop 等待回收用）
}

type backendMgr struct {
	mu           sync.Mutex
	addr         string // 当前 backend 地址（完整，含 token）——代理目标随切换更新
	base         string // 当前 backend 无 token 的 base URL
	token        string // 当前 backend token
	pid          int    // 当前 backend PID
	dir          string // 当前 backend 项目根
	own          bool   // 当前 backend 是否本进程拉起的（退出/切走时决定是否停止）
	done         <-chan struct{}
	backends     map[string]*ownedBackend // dir → 常驻 backend（own 拉起；切换保留）
	projects     []string                 // 已添加的项目目录列表（持久化）
	projectsPath string                   // 项目列表存储文件
	// startBackendFn 可注入（测试用 fake 避免真实拉起 backend）；nil 时用 startBackend。
	startBackendFn func(bin, dir string) (addr string, pid int, done <-chan struct{}, err error)
}

func newBackendMgr() *backendMgr {
	return &backendMgr{projectsPath: projectsFile(), backends: map[string]*ownedBackend{}}
}

// initProjects 启动时加载项目列表，并确保初始目录位于列表首位（默认第一个
// 项目）。initialDir 为空时（.app 打包且无最近使用项目）不 promote——
// 避免项目列表首位出现空字符串（历史 bug：.app 场景默认项目=用户主目录，
// 打包 GUI "自带莫名其妙的项目"）。
func (b *backendMgr) initProjects(initialDir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projects = loadProjectsFrom(b.projectsPath)
	if initialDir != "" {
		b.projects = promoteUnique(b.projects, initialDir)
	}
	_ = saveProjectsTo(b.projectsPath, b.projects)
}

// listProjects 返回项目列表（含当前目录，首项为最近使用在前）。
func (b *backendMgr) listProjects() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.projects))
	out = append(out, b.projects...)
	return out
}

// addProject 添加目录到项目列表（存在性校验由调用方完成；去重 + 持久化）。
func (b *backendMgr) addProject(dir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projects = appendUnique(b.projects, dir)
	_ = saveProjectsTo(b.projectsPath, b.projects)
}

// removeProject 从项目列表移除（持久化）。若移除的是当前目录，返回 true，
// 调用方应切换到列表剩余的第一个项目（或保持当前 backend 不变）。
// 被移除目录的常驻 backend 一并停止（项目已移除，进程无保留意义）。
func (b *backendMgr) removeProject(dir string) bool {
	b.mu.Lock()
	cur := b.dir
	kept := make([]string, 0, len(b.projects))
	for _, p := range b.projects {
		if p != dir {
			kept = append(kept, p)
		}
	}
	b.projects = kept
	_ = saveProjectsTo(b.projectsPath, b.projects)
	b.mu.Unlock()
	b.stopBackend(dir)
	return dir == cur
}

// switchProject 切换到目标项目目录：复用/拉起目标目录的 backend（不杀旧
// backend，旧项目后台进程与会话保留）→ 更新状态与项目列表（当前目录提到
// 列表首位）。
// 注意：前置去重必须移除【所有】旧位置——旧实现 appendUnique 后再前置
// 只跳过第一个匹配，若目录此前已存在于列表（历史添加过），会残留一个
// 重复项（实测 projects 列表出现同一目录两次）。
func (b *backendMgr) switchProject(bin, dir string) (map[string]any, error) {
	res, err := b.openDir(bin, dir)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	kept := make([]string, 0, len(b.projects)+1)
	kept = append(kept, dir)
	for _, p := range b.projects {
		if p != dir {
			kept = append(kept, p)
		}
	}
	b.projects = kept
	_ = saveProjectsTo(b.projectsPath, b.projects)
	b.mu.Unlock()
	return res, nil
}

// appendUnique 追加目录（去重）。
func appendUnique(list []string, dir string) []string {
	for _, p := range list {
		if p == dir {
			return list
		}
	}
	return append(list, dir)
}

// promoteUnique 把目录提到列表首位（去重）：已存在则前移，不存在则插入到首位。
func promoteUnique(list []string, dir string) []string {
	kept := make([]string, 0, len(list)+1)
	kept = append(kept, dir)
	for _, p := range list {
		if p != dir {
			kept = append(kept, p)
		}
	}
	return kept
}

// defaultProjectDir 返回 GUI 默认加载的第一个项目根（优先级）：
//  1. 非 .app 打包：主程序（可执行文件）所在目录（用户要求：主程序所在
//     目录为第一个默认项目）；
//  2. 启动时工作目录（cwd）下存在 .contextloom → 直接以该目录为默认项目——
//     用户在项目目录启动 GUI / `open ContextLoom.app`（终端）时，GUI 立即
//     加载当前项目（读取 .contextloom 里保存好的会话/数据，而非"自带"其他
//     目录）；
//  3. .app 打包场景：最近使用的项目（projects.json 首位存在项）；
//  4. 回退 cwd；最后兜底空。
func defaultProjectDir() string {
	if exe, err := os.Executable(); err == nil {
		if d := defaultProjectDirFromExe(exe); d != "" {
			return d
		}
	}
	if d, err := os.Getwd(); err == nil {
		if fi, err := os.Stat(filepath.Join(d, ".contextloom")); err == nil && fi.IsDir() {
			return d
		}
	}
	if d := lastUsedProject(); d != "" {
		return d
	}
	if d, err := os.Getwd(); err == nil {
		// 防御：根目录 "/" 不是项目（mkdir /.contextloom 只读失败，用户实测
		// "加载对话失败/启动失败"）。Finder/LaunchServices 启动 .app 时
		// CWD 常为 "/"，无 .contextloom 的项目目录不可作为默认项目——返回空
		// 由调用方回退（startBackend 对空 dir 会走当前目录，仍有风险，
		// 但至少不主动选择根目录）。
		if filepath.Clean(d) != string(filepath.Separator) {
			return d
		}
	}
	return ""
}

// defaultProjectDirFromExe 由可执行文件路径推导默认项目根。
//
// .app 打包场景（macOS）：exe 位于 "ContextLoom.app/Contents/MacOS/" 内，
// 应用包目录不是项目根（.contextloom 会话数据写入应用包内部会被签名校验
// 拦截或随应用更新丢失）；【不回退用户主目录】——$HOME 不是项目，把
// $HOME 当默认项目会让项目管理器"自带莫名其妙的项目"（历史 bug：打包
// GUI 打开后项目列表首位是用户主目录，读不到任何 .contextloom 会话）。
// 返回空 → 调用方走"最近使用项目"推导（defaultProjectDir）。
// 非 .app 场景（裸二进制/开发目录）行为不变：返回 exe 所在目录。
func defaultProjectDirFromExe(exe string) string {
	if strings.Contains(exe, "/Contents/MacOS/") {
		return ""
	}
	if d := filepath.Dir(exe); d != "" {
		return d
	}
	return ""
}

// lastUsedProject 返回最近使用的项目（projects.json 首位【存在】项）。
// .app 打包场景默认项目推导用：用户上次使用过的项目优先于任何猜测——
// 这样打包 GUI 打开后直接加载用户真实项目（.contextloom 里的会话立即可见），
// 而不是"自带"应用包目录或主目录。
func lastUsedProject() string {
	list := loadProjectsFrom(projectsFile())
	for _, p := range list {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

// set 记录当前 backend 状态（own=true 表示本进程拉起的）。同时把 own 拉起的
// backend 登记进常驻池（backends[dir]），供切换项目时复用、退出时统一停止。
func (b *backendMgr) set(addr string, pid int, dir string, own bool, done <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addr = addr
	b.pid = pid
	b.dir = dir
	b.own = own
	b.done = done
	if u, err := url.Parse(addr); err == nil {
		b.token = u.Query().Get("token")
		b.base = (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	}
	// 登记进常驻池：本进程【使用中】的 backend 一律登记（own 与否都登记），
	// 保证切换项目来回时复用同一进程（会话/在途流保留）。own 标志随条目
	// 保存——stop() 只终止 own 拉起的进程；复用的（own=false，如启动时
	// 复用残留 backend）不杀（它不属于本进程生命周期）。
	if dir != "" && addr != "" && pid > 0 {
		b.backends[dir] = &ownedBackend{addr: addr, base: b.base, token: b.token, pid: pid, done: done, own: own}
	}
}

// snapshot 返回当前 backend 状态（/api/project/current 用）。
func (b *backendMgr) snapshot() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{
		"dir": b.dir, "addr": b.addr, "pid": b.pid,
	}
}

// currentDir 返回当前 backend 的项目根目录（动态读取，切换项目后立即反映
// 新目录——/api/fs/img 等 GUI 侧路径解析必须用它而非启动时的闭包变量）。
func (b *backendMgr) currentDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dir
}

func (b *backendMgr) baseURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.base
}

// backendAddrFor 返回指定项目目录对应 backend 的完整地址（含 token）；
// 该目录的 backend 未拉起/已停止/不在常驻池时返回 ""（调用方据此决定
// 走当前 backend 或提示前端新建连接）。
// 多项目并行通道（GUI 多连接）的寻址基础：前端为每个打开过的项目维护
// 独立 WS 连接，连接建立前先查目标目录 backend 是否已在常驻池——在池则
// 直接连其地址（秒拉起），不在池则先由 /api/projects/open|switch 拉起。
func (b *backendMgr) backendAddrFor(dir string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ob, ok := b.backends[dir]; ok && ob != nil && ob.addr != "" {
		return ob.addr
	}
	return ""
}

// backendAddrs 返回常驻池内全部已拉起 backend 的目录→完整地址映射
// （/api/projects 附带返回，前端初始化时据此为已打开项目预建连接）。
// 只含 own 拉起的（复用的 own=false 也含——它同样在池中可用）。
func (b *backendMgr) backendAddrs() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]string, len(b.backends))
	for dir, ob := range b.backends {
		if ob != nil && ob.addr != "" {
			out[dir] = ob.addr
		}
	}
	return out
}

func (b *backendMgr) tokenValue() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token
}

// stop 停止本进程拉起的全部 backend（复用的不杀）。
// 常驻池语义：removeProject 移除项目/进程退出时统一停止，切换项目【不调
// stop】（openDir 现在复用池中已有 backend，不重启不杀进程）。
// 历史 bug 与修复要点：
//  1. probeBackend URL 拼接错误导致"误判不存活"→ 跳过清理 → 切换/退出
//     时进程无限堆积（曾实测 16 个残留）。修复后 probe 仅作诊断参考，
//     【不再作为是否清理的条件】——own 的 backend 是我们自己拉起的子进程，
//     对已退出的 PID 发信号返回 ESRCH 无害，因此始终终止确保清理。
//  2. 信号发送必须走平台函数（signalTerminate)：Windows 的
//     Process.Signal 不支持 SIGTERM，旧实现每次都要等 3 秒超时才强杀，
//     切换/退出必卡顿；Windows 改用 taskkill /T /F 立即递归终止。
func (b *backendMgr) stop() {
	b.mu.Lock()
	// 汇聚本进程拉起的 backend（own=true；当前指向 + 常驻池）。
	// 复用的（own=false，如启动时复用其他 GUI 残留的 backend）不杀——
	// 它们不属于本进程生命周期，仅解除池登记（条目清空）。
	type toStop struct {
		pid  int
		done <-chan struct{}
	}
	stops := make([]toStop, 0, len(b.backends)+1)
	seen := map[int]bool{}
	if b.own && b.pid > 0 && !seen[b.pid] {
		stops = append(stops, toStop{b.pid, b.done})
		seen[b.pid] = true
	}
	for _, ob := range b.backends {
		if ob.own && ob.pid > 0 && !seen[ob.pid] {
			stops = append(stops, toStop{ob.pid, ob.done})
			seen[ob.pid] = true
		}
	}
	b.backends = map[string]*ownedBackend{}
	b.mu.Unlock()

	for _, s := range stops {
		_ = signalTerminate(s.pid)
		if s.done == nil {
			continue // 无进程句柄：信号已尽力
		}
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			// 优雅退出超时（backend 卡死）：强制 Kill 防残留
			_ = signalKill(s.pid)
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	// 停完当前 backend 后，把当前指向清掉（状态由 resetBackend/openDir 重建）
	b.mu.Lock()
	b.addr, b.base, b.token, b.pid, b.dir, b.own, b.done = "", "", "", 0, "", false, nil
	b.mu.Unlock()
}

// stopBackend 单独停止指定目录对应的常驻 backend（removeProject 移除项目用），
// 并从池中清除登记。保留其他项目 backend 不受影响（切换/对话仍可用）。
// 若被停止的正是当前 backend（不论 own 与否），当前指向一并清空——调用方
// （remove handler）随后会切换到其他项目或 resetBackend。
func (b *backendMgr) stopBackend(dir string) {
	b.mu.Lock()
	ob, ok := b.backends[dir]
	delete(b.backends, dir)
	if ok && ob != nil && b.dir == dir {
		b.addr, b.base, b.token, b.pid, b.dir, b.own, b.done = "", "", "", 0, "", false, nil
	}
	b.mu.Unlock()
	if ok && ob != nil && ob.pid > 0 && ob.own {
		// 只终止本进程拉起的；复用的（own=false）仅解除登记，不杀——
		// 它可能仍被其他 GUI 实例使用（启动复用场景）。
		_ = signalTerminate(ob.pid)
		if ob.done != nil {
			select {
			case <-ob.done:
			case <-time.After(3 * time.Second):
				_ = signalKill(ob.pid)
				select {
				case <-ob.done:
				case <-time.After(2 * time.Second):
				}
			}
		}
	}
}

// resetBackend 清空 backend 状态（无可用项目时调用：当前目录已移除且剩余
// 项目全部不可达，GUI 回到"未连接"状态等待用户打开/添加新项目）。
// 与 stop 的区别：stop 只终止进程，reset 同时清除地址/目录等状态。
func (b *backendMgr) resetBackend() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addr, b.base, b.token = "", "", ""
	b.pid, b.dir = 0, ""
	b.own, b.done = false, nil
	// 当前目录 backend 已随 stopBackend 停止并从池中清除；池中其他项目
	// backend 保留（它们与当前状态无关，切换时仍可复用）。
}

// openDir 切换到目标项目目录：复用该目录常驻 backend（不重启不杀进程），
// 首次访问才拉起新 backend。常驻池内 backend 进程保留其会话与在途流——
// 切换另一项目时旧项目后台继续运行，切回立即可见（"切换项目右侧会话不
// 关闭"核心）。返回新 backend 快照（dir/addr/pid）。
func (b *backendMgr) openDir(bin, dir string) (map[string]any, error) {
	b.mu.Lock()
	if ob, ok := b.backends[dir]; ok && ob != nil && ob.addr != "" {
		// 已在常驻池：直接复用（仅把代理目标切到它，不碰进程）。
		// own 保持条目原值：复用进程不改变归属（启动复用的残留 backend
		// own=false → 当前指向也 own=false，stop() 不杀它）。
		b.addr = ob.addr
		b.base = ob.base
		b.token = ob.token
		b.pid = ob.pid
		b.dir = dir
		b.own = ob.own
		b.done = ob.done
		b.mu.Unlock()
		_ = saveState(&state{Addr: ob.addr, PID: ob.pid, Dir: dir, TS: time.Now().Unix()})
		return b.snapshot(), nil
	}
	b.mu.Unlock()

	sb := b.startBackendFn
	if sb == nil {
		sb = startBackend
	}
	addr, pid, done, err := sb(bin, dir)
	if err != nil {
		return nil, err
	}
	b.set(addr, pid, dir, true, done)
	_ = saveState(&state{Addr: addr, PID: pid, Dir: dir, TS: time.Now().Unix()})
	return b.snapshot(), nil
}

// wsProxyTo 动态 backend 代理：Director 每次从 mgr 读取当前地址，
// 支持运行时切换项目目录（openDir 后新地址自动生效，无需重建代理）。
// 多项目并行：请求可带 ?dir=<项目目录> 查询参数——命中常驻池内该目录的
// backend 时路由到它（GUI 多连接通道：每个项目一条独立 WS，切换项目不
// 打断其他项目在途流）；无 dir 参数或未命中时回退当前 backend（向后兼容，
// 单连接/旧客户端行为不变）。
func wsProxyTo(b *backendMgr) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			base := b.baseURL()
			token := b.tokenValue()
			// 多项目路由：请求显式携带 dir 参数 → 优先路由到该目录 backend
			if d := r.URL.Query().Get("dir"); d != "" {
				if addr := b.backendAddrFor(d); addr != "" {
					if u, err := url.Parse(addr); err == nil && u.Scheme != "" && u.Host != "" {
						r.URL.Scheme = u.Scheme
						r.URL.Host = u.Host
						r.URL.Path = "/ws"
						q := r.URL.Query()
						q.Set("token", u.Query().Get("token"))
						r.URL.RawQuery = q.Encode()
						r.Host = u.Host
						return
					}
				}
				// 未命中（目录 backend 未拉起）：回退当前 backend——连接仍
				// 建立，由前端在切换完成后重建（openDir 已把该目录登记进池）
			}
			u, err := url.Parse(base)
			if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
				// backend 未就绪/已停止：不转发（前端 WS 自动重连）
				r.URL.Scheme = ""
				r.URL.Host = ""
				return
			}
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
			// 重写路径到 /ws 并注入 token（浏览器只接触 contextloom-gui，token 不泄露）
			r.URL.Path = "/ws"
			q := r.URL.Query()
			q.Set("token", token)
			r.URL.RawQuery = q.Encode()
			r.Host = u.Host
		},
	}
	proxy.ErrorLog = log.New(os.Stderr, "[ws-proxy] ", 0)
	return proxy
}

// noCache 禁用静态资源 HTTP 缓存（每次刷新都重新验证）。
// FileServer 默认 ETag 缓存会让浏览器在 GUI 升级后仍用旧 app.js/style.css
// （旧前端可能有连接类 bug），表现为"初始化异常、刷新后才正常"。
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		h.ServeHTTP(w, r)
	})
}

// projectHandlers 项目目录 API（GUI 自身处理，不代理给 backend）：
//   - GET  /api/project/current → 当前项目目录/backend 地址
//   - POST /api/project/open    → 打开新目录（切换项目根，backend 随之重启）
//   - GET  /api/projects        → 项目列表 + 当前目录
//   - POST /api/projects/add    → 添加目录到列表（不切换）
//   - POST /api/projects/remove → 从列表移除（若为当前目录，自动切到剩余首个）
//   - POST /api/projects/switch → 切换到指定目录（backend 重启，实时切换）
func projectHandlers(b *backendMgr, bin string) http.Handler {
	mux := http.NewServeMux()
	// /api/fs/browse 目录浏览器（项目管理器"以目录选中的形式引入项目"用）：
	// 列出任意绝对路径下的【子目录】（纯只读、不做项目根越界限制——它是
	// GUI 层自由浏览选择项目目录用的，与 backend 的项目根沙箱无关）。
	mux.HandleFunc("/api/fs/browse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimSpace(r.URL.Query().Get("path"))
		type dirEntry struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		resp := map[string]any{"path": p, "parent": "", "dirs": []dirEntry{}}
		listDirs := func(abs string) []dirEntry {
			entries, err := os.ReadDir(abs)
			if err != nil {
				return nil
			}
			dirs := make([]dirEntry, 0, len(entries))
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				dirs = append(dirs, dirEntry{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
			}
			return dirs
		}
		if p == "" {
			if runtime.GOOS == "windows" {
				// Windows 根：列出可用盘符
				var roots []dirEntry
				for _, drv := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
					root := string(drv) + ":\\"
					if _, err := os.Stat(root); err == nil {
						roots = append(roots, dirEntry{Name: root, Path: root})
					}
				}
				resp["path"] = "此电脑"
				resp["dirs"] = roots
			} else {
				resp["path"] = "/"
				resp["dirs"] = listDirs("/")
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			http.Error(w, "无效路径", http.StatusBadRequest)
			return
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			http.Error(w, "目录不存在或不可访问: "+abs, http.StatusBadRequest)
			return
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			parent = "" // 已到文件系统根
		}
		resp["path"] = abs
		resp["parent"] = parent
		resp["dirs"] = listDirs(abs)
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/project/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.snapshot())
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": b.listProjects(),
			"current":  b.snapshot()["dir"],
			// 常驻池 backend 目录→地址映射：前端据此为已打开项目预建/
			// 复用独立 WS 连接（多项目并行通道，切换项目不打断在途流）。
			"backends": b.backendAddrs(),
		})
	})
	// decodePath 解析并校验请求中的目录参数。
	decodePath := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
			http.Error(w, "需要 path 参数", http.StatusBadRequest)
			return "", false
		}
		dir := strings.TrimSpace(body.Path)
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			http.Error(w, "目录不存在或不可访问: "+dir, http.StatusBadRequest)
			return "", false
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			abs = dir
		}
		return abs, true
	}
	// decodePathLoose 只解码并绝对化目录参数，不做存在性校验。
	// 用于移除等场景：目标可能已被删除/移动，仍应允许操作（提示即可）。
	decodePathLoose := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
			http.Error(w, "需要 path 参数", http.StatusBadRequest)
			return "", false
		}
		dir := strings.TrimSpace(body.Path)
		abs, err := filepath.Abs(dir)
		if err != nil {
			abs = dir
		}
		return abs, true
	}
	// switchTo 切换项目（backend 重启），供 open/switch 复用。
	switchTo := func(w http.ResponseWriter, r *http.Request, dir string) {
		res, err := b.switchProject(bin, dir)
		if err != nil {
			http.Error(w, "切换项目目录失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
	// openHandler：打开项目 = 添加（入列表首位）+ 激活（backend 切换重启）。
	// 同时注册单数 /api/project/open（兼容旧端点）与复数 /api/projects/open
	// （与其他 projects 端点命名一致，前端使用复数）。
	openHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "需要 POST", http.StatusMethodNotAllowed)
			return
		}
		dir, ok := decodePath(w, r)
		if !ok {
			return
		}
		switchTo(w, r, dir)
	}
	mux.HandleFunc("/api/project/open", openHandler)
	mux.HandleFunc("/api/projects/open", openHandler)
	mux.HandleFunc("/api/projects/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "需要 POST", http.StatusMethodNotAllowed)
			return
		}
		dir, ok := decodePath(w, r)
		if !ok {
			return
		}
		b.addProject(dir)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "projects": b.listProjects()})
	})
	mux.HandleFunc("/api/projects/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "需要 POST", http.StatusMethodNotAllowed)
			return
		}
		// 移除用宽松解析：目录不存在也要允许移除（提示即可，不能阻止）。
		// 目录可能已被删除/移动/换盘，此时从列表移除是用户明确意图。
		dir, ok := decodePathLoose(w, r)
		if !ok {
			return
		}
		notExists := false
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			notExists = true
		}
		wasCurrent := b.removeProject(dir)
		// 移除的是当前项目：自动切到剩余列表中第一个【可达】的项目。
		// 历史 bug：直接切 rest[0]——若其为脏数据（目录已删除/移动，如
		// 测试残留、外接盘未挂载），startBackend chdir 失败 → 500 →
		// 前端显示"移除失败"（实际移除已成功）。修复：跳过不可达项；
		// 全部不可达则停止 backend 并返回成功（提示，不报错）。
		if wasCurrent {
			var msg string
			switched := false
			for _, cand := range b.listProjects() {
				if st, err := os.Stat(cand); err != nil || !st.IsDir() {
					continue // 残留/不可达：跳过，不阻塞移除
				}
				if _, err := b.switchProject(bin, cand); err != nil {
					msg = fmt.Sprintf("已从列表移除；自动切换到 %s 失败: %v", cand, err)
					continue // 尝试下一个可用项
				}
				switched = true
				break
			}
			if !switched {
				// 剩余项目全部不可达：移除已成功，停止 backend（其目录已被
				// 移除），回到"未连接"状态等待用户打开/添加新项目。
				b.stop()
				b.resetBackend()
				if msg == "" {
					msg = "已从列表移除；剩余项目目录均不存在，未自动切换（可打开/添加新项目）"
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "notExists": notExists, "msg": msg, "projects": b.listProjects(),
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		msg := "已从列表移除"
		if notExists {
			msg = "目录不存在，已从列表移除"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "notExists": notExists, "msg": msg, "projects": b.listProjects()})
	})
	mux.HandleFunc("/api/projects/switch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "需要 POST", http.StatusMethodNotAllowed)
			return
		}
		dir, ok := decodePath(w, r)
		if !ok {
			return
		}
		switchTo(w, r, dir)
	})
	return mux
}

// logDir/logFilePath/appendLog：GUI 程序（Windows 双击、macOS Finder 双击）
// 通常没有控制台，log 输出到 stderr 不可见——所有日志同时写入
// 用户缓存目录下的 gui.log，供诊断"闪退"等启动问题。
func logDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "contextloom-gui")
}

func logFilePath() string { return filepath.Join(logDir(), "gui.log") }

// setupLogFile 让所有 log 输出同时写入 gui.log（stderr 保留给命令行场景）。
func setupLogFile() {
	if err := os.MkdirAll(logDir(), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

// fatalExit 记录致命错误并退出（替代 log.Fatal）。
// 关键：Windows 双击运行（GUI 子系统、无控制台）时 log.Fatal 输出不可见，
// 进程直接消失——用户看到的就是"闪退"。因此必须同时写日志文件 +
// 弹系统错误对话框，让失败原因可见、可查。
func fatalExit(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("致命错误: %s", msg)
	showFatalBox("ContextLoom GUI 启动失败", msg+"\n\n日志文件: "+logFilePath())
	os.Exit(1)
}

func main() {
	// 环境卫生：清除 MallocStackLogging 家族变量，防止 backend 及
	// 会话子进程退出时打印 macOS malloc 噪音污染终端（同 cmd/contextloom）。
	envclean.StripMallocDebug()
	setupLogFile()
	flag.Parse()

	if *flagVersion {
		fmt.Printf("ContextLoom GUI v%s\n", version.Version)
		return
	}

	if *flagStop {
		st, err := loadState()
		if err != nil {
			fatalExit("没有运行中的 contextloom 服务: %v", err)
		}
		if st.PID > 0 {
			_ = signalTerminate(st.PID)
		}
		clearState()
		fmt.Println("已停止 contextloom 后台服务")
		return
	}

	backendAddr := *flagBackend
	// 默认第一个项目：非 .app 场景 = 主程序（可执行文件）所在目录；
	// .app 打包场景 = 最近使用的项目（见 defaultProjectDir）——
	// 保证打包 GUI 打开后直接加载用户真实项目（.contextloom 会话立即可见）。
	// config.yaml 仍跟随 contextloom 可执行文件目录，不受影响。
	dir := defaultProjectDir()
	mgr := newBackendMgr()
	mgr.initProjects(dir)
	var bin string

	// 复用已运行的 backend（秒开），否则拉起。
	// ⚠️ 必须校验 state.Dir 与当前启动项目目录一致（shouldReuseBackend）：
	// 上次 GUI 崩溃/强杀/异常退出会残留 backend 进程，其 state.json 记录
	// 仍在。若不校验目录就复用，在【其他项目】启动的 GUI 会连到残留 backend
	// ——会话/文件树/终端等全部数据写到错误项目，表现为"跨项目会话串联"
	// （历史缺陷：用户在任何项目新建会话都串到残留 backend 所在项目）。
	if backendAddr == "" {
		if st, err := loadState(); err == nil && shouldReuseBackend(st, dir, probeBackend) {
			backendAddr = st.Addr
			dir = st.Dir
			mgr.set(backendAddr, st.PID, dir, false, nil) // 复用：退出/切换时不杀
			log.Printf("复用运行中的 contextloom 服务: %s", backendAddr)
		}
	}
	if backendAddr == "" {
		var err error
		bin, err = findContextLoomBin()
		if err != nil {
			fatalExit("%v", err)
		}
		// 注意：必须用 = 复用外层 backendAddr（不能用 :=，否则在块内新建
		// 局部变量遮蔽外层，导致外层仍是空串，代理/日志拿到空地址）
		var pid int
		var bdone <-chan struct{}
		backendAddr, pid, bdone, err = startBackend(bin, dir)
		if err != nil {
			fatalExit("%v", err)
		}
		mgr.set(backendAddr, pid, dir, true, bdone) // 自己拉起：退出/切换时停止
		_ = saveState(&state{Addr: backendAddr, PID: pid, Dir: dir, TS: time.Now().Unix()})
		log.Printf("已拉起 contextloom 后台服务: %s", backendAddr)
	}
	if bin == "" {
		bin, _ = findContextLoomBin() // 复用场景：projectHandlers 切换目录仍需要 bin
	}

	// 解析 backend 地址与 token（校验合法性；代理经 mgr 动态读取）
	u, err := url.Parse(backendAddr)
	if err != nil {
		mgr.stop() // backend 已拉起：先清理再退出（fatalExit 的 os.Exit 会跳过 defer）
		fatalExit("无效 backend 地址 %q: %v", backendAddr, err)
	}
	_ = u

	// 构建 /api 代理（两种模式共用）：/api/ws → backend /ws（注入 token），
	// /api/health 探活。浏览器模式挂到本地 http mux；桌面模式挂到 Wails
	// assetserver.Handler（窗口内同源请求直达 backend，token 不出进程）。
	// wsProxyTo 支持运行时切换项目目录（openDir 后新地址自动生效）。
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxyTo(mgr))
	proxyMux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"backend":%q}`, mgr.baseURL())
	})
	// 项目目录 API（打开其他目录 / 查询当前项目根 / 多项目管理）
	// 注意：不能 StripPrefix——projectHandlers 内部注册的是完整路径
	// （/api/project/current 等），StripPrefix 会把路径改掉导致 404。
	proxyMux.Handle("/api/project/", projectHandlers(mgr, bin))
	proxyMux.Handle("/api/projects", projectHandlers(mgr, bin))
	proxyMux.Handle("/api/projects/", projectHandlers(mgr, bin))
	// 目录浏览器（项目管理器选择目录用）也走 projectHandlers
	proxyMux.Handle("/api/fs/browse", projectHandlers(mgr, bin))
	// Markdown 预览本地图片代理：/api/fs/img?path=xxx
	// （相对路径以项目目录为基准，仅读、禁逃逸、白名单扩展名）
	proxyMux.HandleFunc("/api/fs/img", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			http.Error(w, "缺少 path", http.StatusBadRequest)
			return
		}
		// 项目根动态读取（切换项目后立即生效，不能闭包启动时的 dir）
		base := mgr.currentDir()
		clean := filepath.Clean(filepath.Join(base, p))
		rel, err := filepath.Rel(base, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			http.Error(w, "路径越界", http.StatusForbidden)
			return
		}
		ext := strings.ToLower(filepath.Ext(clean))
		mime := map[string]string{
			".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
			".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
			".bmp": "image/bmp", ".ico": "image/x-icon", ".avif": "image/avif",
		}[ext]
		if mime == "" {
			http.Error(w, "非图片文件", http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			http.Error(w, "读取失败: "+err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})

	// PlantUML 预览代理：/api/plantuml/svg?src=<plantuml 源码>
	// Markdown 预览中 ```plantuml 代码块渲染为图。前端同源请求本端点，
	// 由本进程 zlib+自定义 base64 编码（~1 格式）后请求配置的 plantuml-server 并返回 SVG——
	// 服务器端请求不受浏览器 CORS 限制，响应再附加 CORS 头，跨域预览双保险；
	// server 地址来自 config.yaml preview 节（GUI 配置面板可改），无 SSRF 面。
	proxyMux.HandleFunc("/api/plantuml/svg", plantumlHandler)

	// 前端资源
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		mgr.stop() // backend 已拉起：先清理再退出
		fatalExit("内嵌前端资源加载失败: %v", err)
	}

	// 桌面窗口模式（默认，Wails/WebView 壳）：内嵌前端 + /api 代理，
	// 不依赖外部浏览器；无 WebView 环境自动回退浏览器模式。
	if !*flagBrowser {
		if err := runDesktop(*flagPort, sub, proxyMux, dir, mgr); err == nil {
			return
		} else if errors.Is(err, errNoFallback) {
			// 不可降级的启动错误（Windows 无 tag 构建 panic）：直接报错
			// 退出，绝不自动打开浏览器（关闭/失败就是失败）。
			mgr.stop() // backend 已拉起：先清理再退出
			fatalExit("%v", err)
		} else {
			log.Printf("桌面窗口不可用（%v），回退浏览器模式…", err)
		}
	}

	// 浏览器模式（--browser 或桌面壳回退）：本地静态服务 + 打开系统浏览器
	fileServer := noCache(http.FileServer(http.FS(sub)))
	mux := http.NewServeMux()
	mux.Handle("/", fileServer)
	mux.Handle("/api/ws", proxyMux)
	mux.Handle("/api/health", proxyMux)
	// 项目目录 API（多项目管理）也走 proxyMux（其上已挂 projectHandlers）
	mux.Handle("/api/project/", proxyMux)
	mux.Handle("/api/projects", proxyMux)
	mux.Handle("/api/projects/", proxyMux)
	mux.Handle("/api/fs/browse", proxyMux)
	// PlantUML 预览代理（跨域双保险：同源代理 + CORS 头）
	mux.Handle("/api/plantuml/", proxyMux)
	// Markdown 预览本地图片代理
	mux.HandleFunc("/api/fs/img", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			http.Error(w, "缺少 path 参数", http.StatusBadRequest)
			return
		}
		base := mgr.currentDir()
		safe := filepath.Join(base, filepath.Clean(strings.TrimPrefix(p, "/")))
		if !strings.HasPrefix(safe, base) {
			http.Error(w, "路径越界", http.StatusForbidden)
			return
		}
		ext := strings.ToLower(filepath.Ext(safe))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		default:
			http.Error(w, "不支持的图片类型", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, safe)
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *flagPort))
	if err != nil {
		mgr.stop() // backend 已拉起：先清理再退出
		fatalExit("监听失败: %v", err)
	}
	guiURL := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	log.Printf("ContextLoom GUI（浏览器模式）: %s（项目目录: %s）", guiURL, dir)

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	if err := openBrowser(guiURL); err != nil {
		log.Printf("自动打开浏览器失败（可手动访问 %s）: %v", guiURL, err)
	}

	// 优雅退出：Ctrl+C 时停止自己拉起的 backend（复用的不杀）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("正在退出…")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	mgr.stop()
}

// errNoFallback 标记"不可降级浏览器"的桌面启动错误（Windows 无 tag 构建的
// panic）。main() 遇到它直接 fatalExit 报错退出，绝不自动打开浏览器——
// 用户诉求：关闭/启动失败就是失败，没道理降级弹出浏览器窗口。
var errNoFallback = errors.New("no-fallback")

// saveWindowSize 保存当前窗口尺寸（下次启动恢复）。
//
// 必须在【窗口仍有效】时调用（OnBeforeClose 阶段）：Wails v2.9.2 的
// App.Run() 流程是 frontend.Run → RunMainLoop → WindowClose（销毁窗口）
// → shutdownCallback(OnShutdown)，OnShutdown 阶段窗口已被销毁，此时调用
// wailsruntime.WindowGetSize 会让 Wails 内部 GetDpiForWindow 返回 0，
// 触发 ScaleToDefaultDPI 除零 panic（历史 bug：关闭 GUI 必现
// "runtime error: integer divide by zero" 弹窗）。
// 内部 recover 兜底：尺寸记忆是锦上添花，任何异常都不影响退出流程。
func saveWindowSize(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("保存窗口尺寸失败（忽略，不影响退出）: %v", r)
		}
	}()
	w, h := wailsruntime.WindowGetSize(ctx)
	if w < 960 || h < 640 {
		return
	}
	st, err := loadState()
	if err != nil || st == nil {
		st = &state{}
	}
	st.WinW = w
	st.WinH = h
	_ = saveState(st)
}

// runDesktop 以 Wails/WebView 桌面窗口模式运行（默认模式，替代浏览器）。
//
// 关键约束：Wails AssetServer 对 WebSocket 请求直接返回 501 Not Implemented
// （assetserver.go: "WebSockets are not supported by the AssetServer"），
// 且对未知路径（/api/config）的 fallback 行为不可靠——因此【不能】把前端
// 页面与 WS 都放在 wails:// scheme 下运行。
//
// 解决方案（v3，最可靠）：Wails 窗口加载一个【极简跳转页】（wails://
// scheme），页面 JS 立即 location.replace 到本地 HTTP 服务
// （http://127.0.0.1:PORT/）。跳转后页面与 WS 全部走本地 HTTP（http 环境），
// 与浏览器模式行为完全一致（同源 WS 直连本地服务，绕过 assetserver 的
// 501 限制），不依赖 assetserver fallback、不依赖跨 scheme fetch。
func runDesktop(port int, sub fs.FS, proxyMux *http.ServeMux, dir string, mgr *backendMgr) (err error) {
	backendAddr := mgr.baseURL()
	// 本地 HTTP 服务变量需提升到 defer 可见范围：panic 回退时关闭，
	// 防止 recover 后遗留孤儿监听端口（浏览器模式再 Listen 不冲突，
	// 但干净退出是生产级要求）。
	var srv *http.Server
	// started：窗口是否成功打开过（OnStartup 触发即视为窗口已就绪）。
	//   - true  + wails.Run 返回任意错误 → 用户已见过窗口（关闭窗口/正常
	//     退出路径），一律视为正常退出返回 nil——【绝不回退浏览器】。
	//   - false + 返回错误 → 窗口从未打开（启动失败），允许降级浏览器。
	// OnStartup 在 Wails 内部 goroutine 中调用，必须用 atomic 防数据竞争。
	var started atomic.Bool

	// 兜底防御（消除 Windows"双击闪退"）：
	//  Wails 在【无 -tags production/dev】构建时，Windows 平台
	//  （app_default_windows.go）的 CreateApp 弹提示框后返回 (nil, nil)，
	//  随后 application.Run() 对 nil App 调用 → 直接 panic 崩溃。
	//  该 panic 属于"构建配置错误"而非"环境不可用"——【不得回退浏览器】
	//  （用户明确要求：关闭/失败就是失败，不降级打开浏览器），而是
	//  包装为 errNoFallback 由 main() fatalExit 弹窗报错退出。
	defer func() {
		if r := recover(); r != nil {
			if srv != nil {
				ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = srv.Shutdown(ctx2)
				cancel()
			}
			if started.Load() {
				// 窗口曾正常打开：panic 发生在退出/关闭阶段（历史根因：
				// OnShutdown 调用 WindowGetSize 触发 Wails 除零 panic）——
				// 按正常退出处理，绝不弹"启动失败"错误框、绝不回退浏览器。
				log.Printf("退出阶段异常（已忽略，进程正常退出）: %v", r)
				mgr.stop() // 兜底清理（OnShutdown 的清理若被 panic 跳过，此处补上）
				err = nil
				return
			}
			mgr.stop() // 窗口从未打开（启动失败）：清理自己拉起的 backend，防止孤儿进程残留
			err = fmt.Errorf("%w: 桌面窗口异常: %v（请用 -tags production 构建 contextloom-gui；Windows 请确认已安装 WebView2 Runtime）", errNoFallback, r)
		}
	}()

	// 本地 HTTP 服务：完整前端静态资源 + WS 代理 + config/health 探活
	// （浏览器模式与桌面模式跳转后共用同一套路由，行为完全一致）。
	mux := http.NewServeMux()
	mux.Handle("/", noCache(http.FileServer(http.FS(sub))))
	mux.Handle("/api/ws", proxyMux)
	mux.Handle("/api/health", proxyMux)
	// 项目目录 API（多项目管理）也走 proxyMux（其上已挂 projectHandlers）
	mux.Handle("/api/project/", proxyMux)
	mux.Handle("/api/projects", proxyMux)
	mux.Handle("/api/projects/", proxyMux)
	mux.Handle("/api/fs/browse", proxyMux)
	// PlantUML 预览代理（桌面模式与浏览器模式一致）
	mux.Handle("/api/plantuml/", proxyMux)
	// Markdown 预览本地图片代理（桌面模式与浏览器模式一致）
	mux.HandleFunc("/api/fs/img", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			http.Error(w, "缺少 path 参数", http.StatusBadRequest)
			return
		}
		base := mgr.currentDir()
		safe := filepath.Join(base, filepath.Clean(strings.TrimPrefix(p, "/")))
		if !strings.HasPrefix(safe, base) {
			http.Error(w, "路径越界", http.StatusForbidden)
			return
		}
		ext := strings.ToLower(filepath.Ext(safe))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		default:
			http.Error(w, "不支持的图片类型", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, safe)
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("本地服务监听失败: %w", err)
	}
	srv = &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	localURL := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)

	// /api/config：兼容旧桌面前端（wails:// 环境探测本地地址），跳转方案下
	// 前端已在 http 环境运行不再需要，但保留兜底。
	configHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"gui":%q}`, localURL)
	})
	mux.HandleFunc("/api/config", configHandler.ServeHTTP)

	// Wails 窗口资源：仅一个跳转页（动态注入本地服务地址）。
	// WKWebView 加载 wails://wails/index.html → JS 立即导航到本地 HTTP。
	redirectHTML := fmt.Sprintf(
		`<!DOCTYPE html><html><head><meta charset="utf-8"><title>ContextLoom</title>`+
			`<script>window.location.replace(%q);</script></head>`+
			`<body style="margin:0;background:#0a0f14;color:#4fd1ff;font-family:ui-monospace,monospace;display:flex;align-items:center;justify-content:center;height:100vh;font-size:14px;">正在启动 ContextLoom GUI…</body></html>`,
		localURL)
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(redirectHTML), Mode: 0o644},
	}

	// 窗口状态记忆：默认 1280×840，优先恢复上次保存的尺寸
	winW, winH := 1280, 840
	if st, err := loadState(); err == nil && st.WinW >= 960 && st.WinH >= 640 {
		winW, winH = st.WinW, st.WinH
	}

	err = wails.Run(&options.App{
		Title:     "ContextLoom GUI",
		Width:     winW,
		Height:    winH,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets:  assets, // 仅跳转页（wails:// scheme），页面随后导航到本地 HTTP
			Handler: configHandler,
		},
		OnStartup: func(ctx context.Context) {
			started.Store(true) // 窗口已成功打开：此后任何返回都视为正常退出
			// 窗口状态记忆：恢复上次尺寸（若 state 有记录且不小于最小尺寸）
			if st, err := loadState(); err == nil && st.WinW >= 960 && st.WinH >= 640 {
				wailsruntime.WindowSetSize(ctx, st.WinW, st.WinH)
			}
			log.Printf("ContextLoom GUI 桌面窗口已就绪（本地服务: %s，backend: %s）", localURL, backendAddr)
		},
		// 窗口状态记忆：在窗口销毁【前】保存尺寸。OnBeforeClose 由 Wails 在
		// 窗口仍有效时回调（Frontend.Quit → OnBeforeClose → winc.Exit），
		// 此时 WindowGetSize 安全（GetDpiForWindow 返回有效 DPI）；返回
		// false = 允许关闭。
		OnBeforeClose: func(ctx context.Context) bool {
			saveWindowSize(ctx)
			return false
		},
		OnShutdown: func(ctx context.Context) {
			// 注意：Wails v2.9.2 的 App.Run 在 WindowClose()（窗口已销毁）
			// 【之后】才调用 OnShutdown，此阶段【禁止】调用任何窗口 API——
			// WindowGetSize 会因 GetDpiForWindow 返回 0 触发 Wails 内部
			// ScaleToDefaultDPI 除零 panic（历史 bug 根因：关闭 GUI 必报
			// "integer divide by zero"）。尺寸保存已移到 OnBeforeClose，
			// 这里只做进程清理。
			// 整体 recover 兜底：清理逻辑（关 HTTP/停 backend）绝不能被
			// 意外 panic 中断（否则遗留孤儿 backend 进程）。
			defer func() {
				if r := recover(); r != nil {
					log.Printf("退出清理异常（已忽略）: %v", r)
				}
			}()
			// 关闭本地 HTTP 服务（窗口关闭即退出）
			ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx2)
			// 停止自己拉起的 backend（复用的不杀）
			mgr.stop()
		},
	})
	if err != nil && started.Load() {
		// 窗口已成功打开过：wails.Run 返回（无论 nil/error）都属于
		// "用户关闭窗口/应用退出"的正常路径——返回 nil 让 main() 直接
		// 退出进程，【绝不回退浏览器】（关闭就要关闭全部）。
		log.Printf("桌面窗口已关闭，正在退出（backend 清理中…）")
		return nil
	}
	return err
}

// stopOwnBackend 停止 contextloom-gui 自己拉起的 backend（own=true 时）。
// 先 probe 确认该地址确实是活着的服务，再发信号——防止 state.json 中
// 的旧 PID 被系统复用而误杀无关进程。
func stopOwnBackend(own bool) {
	if !own {
		return
	}
	st, err := loadState()
	if err != nil || st == nil || st.PID <= 0 {
		return
	}
	if !probeBackend(st.Addr) {
		return
	}
	_ = signalTerminate(st.PID)
}

// openBrowser 打开系统默认浏览器。
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
		cmd.SysProcAttr = hiddenProcAttr() // 无控制台父进程下禁止 cmd 新建黑窗
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 回收退出状态：open/xdg-open 是短暂命令，Start 后不 Wait 会留下
	// 僵尸进程（<defunct>）——历史同类问题（startBackend 成功路径）。
	go func() { _ = cmd.Wait() }()
	return nil
}

/* ═══════════ PlantUML 预览代理 ═══════════ */

// encodePlantUML 将 plantuml 源码编码为 plantuml-server URL 片段：
// UTF-8 → 完整 zlib 流（Java DeflaterOutputStream 兼容，带 2 字节头+adler32）
// → 自定义 base64（PlantUML 字母表），前缀 ~1。
//
// 为什么不用 ~h（deflate+hex）？实测（2026-08）内网自建服务器与
// plantuml.com 对 ~h 均返回 400（X-PlantUML-Diagram-Error: Syntax Error?），
// 而 ~1 返回 200 正常渲染——~1 是 PlantUML 最广泛兼容的 legacy 格式，
// 所有 plantuml-server 版本均支持，零兼容风险。
func encodePlantUML(text string) string {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write([]byte(text))
	_ = zw.Close()
	return "~1" + plantumlBase64.EncodeToString(buf.Bytes())
}

// plantumlBase64：PlantUML 自定义 base64 字母表
// （与标准 base64 的字母表映射不同：数字在前，-/ 替代 +/）。
var plantumlBase64 = base64.NewEncoding("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_")

// previewCfgCache 缓存 preview 配置（mtime 变化自动重读）——
// GUI 配置面板保存 preview.plantuml_server/enabled 后无需重启即生效。
var (
	previewMu  sync.Mutex
	previewCfg = struct {
		path    string
		mtime   time.Time
		server  string
		enabled bool
	}{server: "https://www.plantuml.com/plantuml", enabled: true}
)

func loadPreviewCfg() (server string, enabled bool) {
	previewMu.Lock()
	defer previewMu.Unlock()
	// 与 backend 同一配置来源（findGUIConfigPath），保证 GUI 预览与 backend
	// 读到的是同一份 config.yaml（此前用 config.DefaultConfigPath() 不含 .app
	// 同级目录，且与 backend 定位可能不一致）。
	p := findGUIConfigPath()
	if p == "" {
		// 无配置文件 → 内置默认（与 config.applyDefaults 一致）
		previewCfg.path = ""
		previewCfg.mtime = time.Time{}
		return previewCfg.server, previewCfg.enabled
	}
	fi, err := os.Stat(p)
	if previewCfg.path == p && !fi.ModTime().After(previewCfg.mtime) {
		return previewCfg.server, previewCfg.enabled // 缓存命中
	}
	cfg, err := config.Load(p)
	if err != nil {
		return previewCfg.server, previewCfg.enabled // 加载失败沿用旧值
	}
	previewCfg.path = p
	previewCfg.mtime = fi.ModTime()
	previewCfg.server = cfg.Preview.PlantUMLServer
	previewCfg.enabled = cfg.Preview.PlantUMLEnabled == nil || *cfg.Preview.PlantUMLEnabled
	return previewCfg.server, previewCfg.enabled
}

// plantumlURL 根据用户配置的 plantuml-server 地址拼接渲染 URL。
// 兼容三种配置形态：
//
//	https://host                  → https://host/svg/~1<base64>
//	https://host/plantuml         → https://host/plantuml/svg/~1<base64>
//	https://host/svg/             → https://host/svg/~1<base64>（已含 /svg 段不重复追加）
func plantumlURL(server, encoded string) string {
	base := strings.TrimRight(server, "/")
	prefix := "/svg/"
	if strings.HasSuffix(base, "/svg") {
		prefix = "/"
	}
	return base + prefix + encoded
}

// plantumlHandler 处理 /api/plantuml/svg?src=<源码>：
// 本地编码 → 请求配置的 plantuml-server → 透传 SVG（+CORS 头 +缓存）。
// server 地址仅来自配置文件（用户自持），src 只作编码输入不拼接 URL，无 SSRF。
func plantumlHandler(w http.ResponseWriter, r *http.Request) {
	server, enabled := loadPreviewCfg()
	if !enabled {
		http.Error(w, `plantuml 渲染已禁用（preview.plantuml_enabled=false）`, http.StatusForbidden)
		return
	}
	src := r.URL.Query().Get("src")
	if src == "" {
		http.Error(w, "缺少 src 参数", http.StatusBadRequest)
		return
	}
	if len(src) > 64*1024 {
		http.Error(w, "src 过长（>64KB）", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		http.Error(w, "plantuml_server 必须为 http(s) 地址", http.StatusBadGateway)
		return
	}
	// server 可能配置为三种形态：
	//   ① 根地址（https://host）        → 追加 /svg/
	//   ② 标准根（https://host/plantuml）→ 追加 /svg/（plantuml.com 规范）
	//   ③ 已含 /svg 段（https://host/svg/）→ 直接拼 /（用户已给完整服务前缀）
	// 统一为 server/svg/~1<base64> 或 server/~1<base64>，避免 /svg/svg 双段错位。
	u := plantumlURL(server, encodePlantUML(src))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		http.Error(w, "构造请求失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "contextloom-gui/"+version.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "plantuml server 请求失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("plantuml server 返回 %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		http.Error(w, "读取响应失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Access-Control-Allow-Origin", "*") // 跨域双保险：同源代理 + CORS
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}
