//go:build darwin

// Wails v2.9.2 的 darwin 前端（WailsContext.m）使用 UTType
// （UniformTypeIdentifiers.framework，macOS 11+），但其构建脚本未把该
// framework 加入链接。在 macOS 15 SDK 下编译会报：
//
//	Undefined symbols for architecture arm64: "_OBJC_CLASS_$_UTType"
//
// 此处显式补上链接。Windows/Linux 无此问题。
package main

/*
#cgo LDFLAGS: -framework UniformTypeIdentifiers
*/
import "C"
