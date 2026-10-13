// Package version 集中定义 ContextLoom 自身版本号，供 CLI（--version）/ MCP Server（serverInfo）等运行时输出引用，
// 避免多处置版本号导致漂移（此前 main.go v2.1.0 / mcp.go 2.2.0 不一致）。
package version

// Version 是 ContextLoom 当前版本号。
const Version = "2.4.0"
