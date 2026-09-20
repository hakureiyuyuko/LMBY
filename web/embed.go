// Package web 内嵌前端构建产物。
//
// 构建流程：cd web && npm run build 会把 Vite 产物写到 web/dist/，
// 随后 go build 时通过 go:embed 打进二进制，运行期不需要 Node。
// 仓库里保留一个占位 index.html，保证没构建前端时 go build 依然能通过。
package web

import "embed"

//go:embed all:dist
var FS embed.FS
