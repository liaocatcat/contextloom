package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 项目列表持久化与增删切换逻辑单元测试（不启动真实 backend）。

func TestProjectsStore_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	mgr := newBackendMgr()
	mgr.projectsPath = p

	mgr.initProjects("/proj/a")
	if got := len(mgr.listProjects()); got != 1 {
		t.Fatalf("initProjects 后应 1 个项目，got %d", got)
	}
	if got := mgr.listProjects()[0]; got != "/proj/a" {
		t.Fatalf("初始项目应为 /proj/a, got %s", got)
	}

	// 添加（去重 + 持久化）
	mgr.addProject("/proj/b")
	mgr.addProject("/proj/a") // 重复：忽略
	if got := len(mgr.listProjects()); got != 2 {
		t.Fatalf("添加后应 2 个项目，got %d", got)
	}
	// 重新加载（模拟 GUI 重启）：从文件恢复
	mgr2 := newBackendMgr()
	mgr2.projectsPath = p
	mgr2.initProjects("/proj/c") // 重启后初始目录加入
	got := mgr2.listProjects()
	if len(got) != 3 {
		t.Fatalf("重启后应 3 个项目，got %d: %v", len(got), got)
	}
	// 初始目录应位于列表首位（默认第一个项目）
	if got[0] != "/proj/c" {
		t.Fatalf("初始目录应为首位, got %v", got)
	}
}

func TestProjectsStore_Remove(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	mgr := newBackendMgr()
	mgr.projectsPath = p
	mgr.initProjects("/proj/a")
	mgr.addProject("/proj/b")
	mgr.addProject("/proj/c")

	// 移除非当前目录：返回 false
	mgr.set("http://127.0.0.1:1/?token=x", 0, "/proj/a", true, nil)
	if cur := mgr.removeProject("/proj/b"); cur {
		t.Fatal("移除非当前目录不应返回 true")
	}
	if got := len(mgr.listProjects()); got != 2 {
		t.Fatalf("移除后应 2 个项目，got %d", got)
	}
	// 移除当前目录：返回 true，列表只剩 1 个
	if cur := mgr.removeProject("/proj/a"); !cur {
		t.Fatal("移除当前目录应返回 true")
	}
	if got := len(mgr.listProjects()); got != 1 {
		t.Fatalf("移除当前后应剩 1 个项目，got %d", got)
	}
}

func TestProjectsStore_AppendUnique(t *testing.T) {
	got := appendUnique(appendUnique(nil, "/a"), "/a")
	if len(got) != 1 {
		t.Fatalf("去重失败: %v", got)
	}
	got = appendUnique(got, "/b")
	if len(got) != 2 {
		t.Fatalf("追加失败: %v", got)
	}
}

func TestProjectsStore_PromoteUnique(t *testing.T) {
	// 新目录 → 插入首位
	got := promoteUnique([]string{"/a", "/b"}, "/c")
	if len(got) != 3 || got[0] != "/c" {
		t.Fatalf("新目录应插入首位: %v", got)
	}
	// 已存在 → 前移到首位且不重复
	got = promoteUnique([]string{"/a", "/b", "/c"}, "/b")
	if len(got) != 3 || got[0] != "/b" {
		t.Fatalf("已存在目录应前移首位且去重: %v", got)
	}
	// 空列表 → 单元素
	got = promoteUnique(nil, "/x")
	if len(got) != 1 || got[0] != "/x" {
		t.Fatalf("空列表插入失败: %v", got)
	}
}

func TestDefaultProjectDir(t *testing.T) {
	d := defaultProjectDir()
	if d == "" {
		t.Fatal("defaultProjectDir 不应为空")
	}
	if !filepath.IsAbs(d) {
		t.Fatalf("defaultProjectDir 应为绝对路径: %s", d)
	}
	// 可执行文件目录应存在（os.Executable 正常时）
	if exe, err := os.Executable(); err == nil {
		if want := filepath.Dir(exe); want != d {
			// 允许回退 cwd 的场景（极端环境），但两者都应非空绝对路径
			t.Logf("defaultProjectDir=%s, exeDir=%s", d, want)
		}
	}
}

func TestProjectsFile_UserConfigDir(t *testing.T) {
	f := projectsFile()
	if f == "" {
		t.Fatal("projectsFile 不应为空")
	}
	if !filepath.IsAbs(f) {
		t.Fatalf("projectsFile 应为绝对路径: %s", f)
	}
	if filepath.Base(filepath.Dir(f)) != "contextloom-gui" {
		t.Fatalf("应存于 contextloom-gui 配置目录: %s", f)
	}
	// 可写性（临时替换不写真实目录）
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatalf("配置目录不可创建: %v", err)
	}
}

// TestProjects_RemoveNonExistentDir：移除不存在的目录不应被阻止——
// 返回 ok + notExists 提示，列表照常移除（用户明确意图优先于存在性校验）。
func TestProjects_RemoveNonExistentDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	mgr := newBackendMgr()
	mgr.projectsPath = p
	mgr.initProjects("/proj/a")
	mgr.addProject("/proj/b")
	gone := filepath.Join(t.TempDir(), "已被删除的目录") // 确定不存在
	mgr.set("http://127.0.0.1:1/?token=x", 0, "/proj/a", true, nil)

	h := projectHandlers(mgr, "contextloom")
	body := mustPathJSON(t, gone)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("移除不存在目录应 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool   `json:"ok"`
		NotExists bool   `json:"notExists"`
		Msg       string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !resp.OK || !resp.NotExists {
		t.Fatalf("应 ok=true 且 notExists=true，got %+v", resp)
	}
	// 列表应已移除该目录（且不误删其他）
	projs := mgr.listProjects()
	for _, x := range projs {
		if x == gone {
			t.Fatal("不存在目录仍留在列表")
		}
	}
	if len(projs) != 2 {
		t.Fatalf("应剩 2 个项目（/proj/a /proj/b），got %v", projs)
	}
}

// switchProject 切换项目时项目列表前置去重：目录此前已添加过（列表中已
// 存在）时，切换后【不得出现重复项】——旧实现 appendUnique 后再前置，
// 只跳过第一个匹配，残留一个重复项（实测 bug）。
func TestSwitchProject_DedupOnRepeatSwitch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	mgr := newBackendMgr()
	mgr.projectsPath = p
	mgr.initProjects("/proj/a")
	mgr.addProject("/proj/b") // /proj/b 已存在于列表
	// 注入 fake backend（不真实拉起进程）
	mgr.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		done := make(chan struct{})
		close(done)
		return "http://127.0.0.1:1/?token=x", 12345, done, nil
	}
	// 切换到已添加过的 /proj/b
	res, err := mgr.switchProject("bin", "/proj/b")
	if err != nil {
		t.Fatalf("switchProject 失败: %v", err)
	}
	if res["dir"] != "/proj/b" {
		t.Fatalf("当前目录应为 /proj/b, got %v", res["dir"])
	}
	got := mgr.listProjects()
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d] {
			t.Fatalf("项目列表出现重复项 %s: %v", d, got)
		}
		seen[d] = true
	}
	if len(got) != 2 {
		t.Fatalf("应恰好 2 个项目（去重）, got %d: %v", len(got), got)
	}
	if got[0] != "/proj/b" {
		t.Fatalf("切换后的目录应位于列表首位, got %v", got)
	}
	// 再次切换同一目录：仍无重复、顺序稳定
	if _, err := mgr.switchProject("bin", "/proj/b"); err != nil {
		t.Fatalf("重复切换失败: %v", err)
	}
	got = mgr.listProjects()
	if len(got) != 2 {
		t.Fatalf("重复切换后应仍为 2 个项目, got %d: %v", len(got), got)
	}
	// 持久化文件同步无重复
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 projects.json 失败: %v", err)
	}
	if strings.Count(string(data), "/proj/b") != 1 {
		t.Fatalf("projects.json 应只含一个 /proj/b: %s", data)
	}
}

func TestDefaultProjectDir_AppBundle(t *testing.T) {
	// .app 打包场景：exe 在 Contents/MacOS 内 → 返回空（不写入应用包、
	// 不回退 $HOME——$HOME 不是项目，回退会让项目管理器"自带莫名其妙的
	// 项目"，历史 bug）。调用方 defaultProjectDir 据此走"最近使用项目"。
	got := defaultProjectDirFromExe("/Users/tester/Applications/ContextLoom.app/Contents/MacOS/contextloom-gui")
	if got != "" {
		t.Fatalf(".app 场景应返回空（走最近使用项目推导）: got=%q", got)
	}
	// 普通场景：exe 所在目录
	got = defaultProjectDirFromExe("/opt/contextloom/contextloom-gui")
	if got != "/opt/contextloom" {
		t.Fatalf("普通场景应返回 exe 目录: got=%q", got)
	}
	// 空输入：filepath.Dir("") 返回 "."（与原逻辑一致，非空即合法）
	if got := defaultProjectDirFromExe(""); got != "." {
		t.Fatalf("空输入应回退 '.'(filepath.Dir 语义): %q", got)
	}
}

func TestLastUsedProject(t *testing.T) {
	// lastUsedProject 读取 projects.json 首位【存在】项；目录不可达时跳过。
	// 用 wWinUserCfg 隔离用户配置目录（不污染真实 projects.json）。
	restore := wWinUserCfg(t)
	defer restore()

	missing := filepath.Join(os.TempDir(), fmt.Sprintf("dm-test-gone-%d", time.Now().UnixNano())) // 不存在
	projA := t.TempDir()                                                                          // 存在
	projB := t.TempDir()
	if err := saveProjectsTo(projectsFile(), []string{missing, projA, projB}); err != nil {
		t.Fatal(err)
	}
	got := lastUsedProject()
	if got != projA {
		t.Fatalf("应跳过不可达项取首位存在项: got=%q want=%q", got, projA)
	}

	// 全部不可达 → 空
	if err := saveProjectsTo(projectsFile(), []string{missing, filepath.Join(t.TempDir(), "x")}); err != nil {
		t.Fatal(err)
	}
	if got := lastUsedProject(); got != "" {
		t.Fatalf("全部不可达应返回空: got=%q", got)
	}

	// 文件不存在 → 空
	if err := os.Remove(projectsFile()); err != nil {
		t.Fatal(err)
	}
	if got := lastUsedProject(); got != "" {
		t.Fatalf("无配置文件应返回空: got=%q", got)
	}
}

// TestDefaultProjectDir_RootFallbackGuard：.app 打包 + 无最近项目 + CWD=/
// 时，defaultProjectDir 不应回退到根目录 "/"（mkdir /.contextloom 只读失败，
// 用户实测 "加载对话失败"）。此测试把 CWD 切到临时目录并清空项目列表，
// 验证 defaultProjectDir 在无可选项目时不返回根目录。
func TestDefaultProjectDir_RootFallbackGuard(t *testing.T) {
	restore := wWinUserCfg(t)
	defer restore()
	// 清空最近使用项目
	if err := os.Remove(projectsFile()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// 把 CWD 切到无 .contextloom 的临时目录（模拟 Finder 启动 CWD=/ 的等价场景）
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got := defaultProjectDir()
	if got == "" {
		t.Log("defaultProjectDir 返回空（调用方走 startBackend 当前目录兜底）")
		return
	}
	if filepath.Clean(got) == string(filepath.Separator) {
		t.Fatalf("defaultProjectDir 不应回退到根目录 '/'，got %q", got)
	}
}
