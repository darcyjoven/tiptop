module github.com/darcyjoven/tiptop

go 1.24

require (
	github.com/darcyjoven/bdl-lsp v0.3.1
	github.com/sijms/go-ora/v2 v2.9.0
)

require (
	github.com/darcyjoven/tree-sitter-bdl v1.0.1 // indirect
	github.com/darcyjoven/tree-sitter-per v1.0.0 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/tree-sitter/go-tree-sitter v0.25.0 // indirect
)

// 本地开发：tiptop 与 bdl-lsp 并行开发时直接指向同级目录。
// 发布时删掉这一行并改用真实版本号。
replace github.com/darcyjoven/bdl-lsp => ../bdl-lsp
