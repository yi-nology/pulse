package feishu

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// warnWriter 输出非致命警告（如甘特视图创建失败、自动同步失败）；包级变量便于测试注入捕获。
var warnWriter io.Writer = os.Stderr

// SetWarnWriter 更换包级警告输出目标；CLI 接线时把警告导向当前命令的 stderr，
// 使"写命令失败仅警告"的文案可被捕获与断言。
func SetWarnWriter(w io.Writer) { warnWriter = w }

// 飞书多维表格字段类型编号（api.go Field.Type）。
const (
	fieldTypeText         = 1 // 文本
	fieldTypeNumber       = 2 // 数字
	fieldTypeSingleSelect = 3 // 单选
	fieldTypeDate         = 5 // 日期
	fieldTypeCheckbox     = 7 // 复选框
)

// taskTableFields / versionTableFields 是 bind 建表用的字段定义。
// 日期列用日期类型（映射层写毫秒时间戳、读回转 YYYY-MM-DD，甘特视图开箱可用）；
// 状态/优先级/版本/人员类用单选——人员选项即名单（本地成员含 agent，故不用飞书
// 用户字段；Bitable 侧手动新增的选项经同步会自动注册为本地成员）。
func taskTableFields() []Field {
	return []Field{
		{Name: "任务名", Type: fieldTypeText},
		{Name: "状态", Type: fieldTypeSingleSelect},
		{Name: "负责人", Type: fieldTypeSingleSelect},
		{Name: "优先级", Type: fieldTypeSingleSelect},
		{Name: "开始", Type: fieldTypeDate},
		{Name: "截止", Type: fieldTypeDate},
		{Name: "预估人日", Type: fieldTypeNumber},
		{Name: "版本", Type: fieldTypeSingleSelect},
		{Name: "已废弃", Type: fieldTypeCheckbox},
		{Name: "updated_by", Type: fieldTypeText},
	}
}

func versionTableFields() []Field {
	return []Field{
		{Name: "版本名", Type: fieldTypeText},
		{Name: "目标日期", Type: fieldTypeDate},
		{Name: "状态", Type: fieldTypeSingleSelect},
		{Name: "备注", Type: fieldTypeText},
		// 进度四列由 pulse 按 tasks 实时计算写入（pull 侧只读忽略），
		// 版本表在飞书侧即是"活"的版本规划视图。
		{Name: "任务数", Type: fieldTypeNumber},
		{Name: "已完成", Type: fieldTypeNumber},
		{Name: "逾期", Type: fieldTypeNumber},
		{Name: "完成度", Type: fieldTypeNumber},
	}
}

// memberTableFields 是成员表（单向镜像）的建表字段：名单与周容量供人在飞书侧
// 查看；维护（增删改成员/容量）走 CLI/MCP，飞书侧修改不回流。
func memberTableFields() []Field {
	return []Field{
		{Name: "姓名", Type: fieldTypeText},
		{Name: "类型", Type: fieldTypeSingleSelect}, // human | agent
		{Name: "周容量人日", Type: fieldTypeNumber},
		{Name: "备注", Type: fieldTypeText},
	}
}

// v11TableFields 是 v1.1 六实体的建表字段定义：全部 text 列 + 已废弃 checkbox +
// updated_by（与任务表约定一致——updated_by 由 pulse 预留、不参与同步）。列名与
// mapping_v11.go 的映射一一对应。
// v11FieldType 按列名给出原生类型：日期列（评审/会议时间、发布时间）用日期、
// 枚举列（状态/结论/类型/严重级/版本引用）与人员列用单选（人员选项即名单，
// 随指派自动创建；Bitable 侧手动新增的选项经同步会自动注册为本地成员）、其余文本。
func v11FieldType(column string) int {
	switch column {
	case "评审时间", "时间", "发布时间":
		return fieldTypeDate
	case "状态", "结论", "评审类型", "严重级", "优先级", "版本", "发现版本",
		"负责人", "提测人", "测试负责人", "发布负责人":
		return fieldTypeSingleSelect
	default:
		return fieldTypeText
	}
}

func v11TableFields(columns []string) []Field {
	fields := make([]Field, 0, len(columns)+2)
	for _, c := range columns {
		fields = append(fields, Field{Name: c, Type: v11FieldType(c)})
	}
	fields = append(fields,
		Field{Name: "已废弃", Type: fieldTypeCheckbox},
		Field{Name: "updated_by", Type: fieldTypeText},
	)
	return fields
}

// v11Tables 描述一张六实体表：Bitable 表名、映射列（已废弃/updated_by 由
// v11TableFields 统一追加）、feishu_tables_json 里的键与写入目标。
type v11Table struct {
	name    string
	columns []string
	set     func(*store.FeishuTables, string)
}

// v11TableDefs 六实体表的定义序列。v1.2 列：需求表 需求UID（全局身份）；bug/提测表
// 需求ID（需求 UID 引用，与评审表既有列同名同义——跨机按 UID 解析，修复本地 id
// 撞号错链）。
func v11TableDefs() []v11Table {
	return []v11Table{
		{"需求表", []string{"需求名", "状态", "负责人", "优先级", "描述", "需求UID"},
			func(t *store.FeishuTables, id string) { t.Requirements = id }},
		{"评审表", []string{"评审类型", "结论", "评审时间", "需求ID"},
			func(t *store.FeishuTables, id string) { t.Reviews = id }},
		{"会议表", []string{"会议标题", "时间"},
			func(t *store.FeishuTables, id string) { t.Meetings = id }},
		{"bug表", []string{"标题", "严重级", "状态", "负责人", "发现版本", "需求ID"},
			func(t *store.FeishuTables, id string) { t.Bugs = id }},
		{"提测表", []string{"版本", "状态", "提测人", "测试负责人", "范围", "需求ID"},
			func(t *store.FeishuTables, id string) { t.TestSubmissions = id }},
		{"发版表", []string{"版本", "状态", "发布负责人", "发布时间", "备注"},
			func(t *store.FeishuTables, id string) { t.Releases = id }},
	}
}

// Bind 为项目创建共享 base（任务表+版本表+甘特视图+v1.1 六实体表）与沉淀文档，
// token 经 SaveProject/SaveFeishuTables 写回项目记录。幂等：项目已有 bitable_app_token
// 时直接原样返回，零 API 调用、零写库。
// 调用序列：AppCreate(项目名) → TableCreate(任务表) → TableCreate(版本表) →
// ViewCreate(甘特, 失败仅警告不中断) → TableCreate×6(六实体表) → DocCreate →
// SaveFeishuTables → SaveProject。
// 说明：甘特视图依赖日期类型的「开始/截止」列，本工具按计划决策建的是文本列，
// 由调用方（CLI）在输出里提示用户做一次手动类型切换。
func Bind(ctx context.Context, c *Client, s *store.Store, p model.Project) (model.Project, error) {
	if p.FeishuBitableAppToken != "" {
		return p, nil // 幂等
	}
	api := c.API()

	appToken, err := api.AppCreate(ctx, p.Name)
	if err != nil {
		return model.Project{}, fmt.Errorf("创建 base 失败: %w", err)
	}
	taskTableID, err := api.TableCreate(ctx, appToken, "任务表", taskTableFields())
	if err != nil {
		return model.Project{}, fmt.Errorf("创建任务表失败: %w", err)
	}
	versionTableID, err := api.TableCreate(ctx, appToken, "版本表", versionTableFields())
	if err != nil {
		return model.Project{}, fmt.Errorf("创建版本表失败: %w", err)
	}
	// 甘特视图是体验增强，失败仅警告（spec：降级为提示用户手动建一次）。
	if err := api.ViewCreate(ctx, appToken, taskTableID, "甘特", "gantt"); err != nil {
		fmt.Fprintf(warnWriter, "警告: 创建甘特视图失败（可在 Bitable 中手动创建一次）: %v\n", err)
	}
	// v1.1 六实体表：交付闭环的沉淀表，字段=各实体映射列（全 text + 已废弃 + updated_by）
	tables := store.FeishuTables{}
	for _, def := range v11TableDefs() {
		tableID, err := api.TableCreate(ctx, appToken, def.name, v11TableFields(def.columns))
		if err != nil {
			return model.Project{}, fmt.Errorf("创建%s失败: %w", def.name, err)
		}
		def.set(&tables, tableID)
	}
	// 成员表（单向镜像）：名单与容量供人在飞书侧查看；维护仍走 CLI/MCP。
	memberTableID, err := api.TableCreate(ctx, appToken, "成员表", memberTableFields())
	if err != nil {
		return model.Project{}, fmt.Errorf("创建成员表失败: %w", err)
	}
	tables.Members = memberTableID
	docToken, err := api.DocCreate(ctx, "", p.Name+" 沉淀文档")
	if err != nil {
		return model.Project{}, fmt.Errorf("创建沉淀文档失败: %w", err)
	}

	// 先写 feishu_tables_json 再落 app token（SaveProject）：六实体表 id 写回失败时
	// 项目仍未标记绑定，重跑 bind 可重来；顺序反了会留下"已绑定但六表 id 丢失"的半态。
	if err := s.SaveFeishuTables(p.ID, tables); err != nil {
		return model.Project{}, fmt.Errorf("写回项目六实体表 id 失败: %w", err)
	}
	p.FeishuBitableAppToken = appToken
	p.FeishuTaskTableID = taskTableID
	p.FeishuVersionTableID = versionTableID
	p.FeishuDocToken = docToken
	if err := s.SaveProject(p); err != nil {
		return model.Project{}, fmt.Errorf("写回项目 token 失败: %w", err)
	}
	return p, nil
}
