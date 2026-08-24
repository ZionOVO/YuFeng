// Package store 提供三本账持久化：连接池、goose 数据库迁移与查询入口。
//
// 新查询只进 query.sql，停止新增内联结构化查询语言；存量手写查询逐步迁入 sqlc 生成层。
//
// [协议与实现术语]: ../../docs/glossary.md#protocol-and-implementation-terms
package store
