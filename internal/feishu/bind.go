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
	fieldTypeText     = 1 // 文本
	fieldTypeNumber   = 2 // 数字
	fieldTypeCheckbox = 7 // 复选框
)

// taskTableFields / versionTableFields 是 bind 建表用的字段定义。
// 计划决策：日期与单选一律用 text 字段落 Bitable——避免日期/单选类型在 API 写入时的格式
// 约束，换取 TaskToFields/FieldsToTask 映射零歧义；甘特视图对文本日期列不可用时，用户在
// Bitable 里把「开始/截止」列改为日期类型即可（一次性手动操作，bind 输出里提示）。
func taskTableFields() []Field {
	return []Field{
		{Name: "任务名", Type: fieldTypeText},
		{Name: "状态", Type: fieldTypeText},
		{Name: "负责人", Type: fieldTypeText},
		{Name: "优先级", Type: fieldTypeText},
		{Name: "开始", Type: fieldTypeText},
		{Name: "截止", Type: fieldTypeText},
		{Name: "预估人日", Type: fieldTypeNumber},
		{Name: "版本", Type: fieldTypeText},
		{Name: "已废弃", Type: fieldTypeCheckbox},
		{Name: "updated_by", Type: fieldTypeText},
	}
}

func versionTableFields() []Field {
	return []Field{
		{Name: "版本名", Type: fieldTypeText},
		{Name: "目标日期", Type: fieldTypeText},
		{Name: "状态", Type: fieldTypeText},
		{Name: "备注", Type: fieldTypeText},
	}
}

// v11TableFields 是 v1.1 六实体的建表字段定义：全部 text 列 + 已废弃 checkbox +
// updated_by（与任务表约定一致——updated_by 由 pulse 预留、不参与同步）。列名与
// mapping_v11.go 的映射一一对应。
func v11TableFields(columns []string) []Field {
	fields := make([]Field, 0, len(columns)+2)
	for _, c := range columns {
		fields = append(fields, Field{Name: c, Type: fieldTypeText})
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

func v11TableDefs() []v11Table {
	return []v11Table{
		{"需求表", []string{"需求名", "状态", "负责人", "优先级", "描述"},
			func(t *store.FeishuTables, id string) { t.Requirements = id }},
		{"评审表", []string{"评审类型", "结论", "评审时间", "需求ID"},
			func(t *store.FeishuTables, id string) { t.Reviews = id }},
		{"会议表", []string{"会议标题", "时间"},
			func(t *store.FeishuTables, id string) { t.Meetings = id }},
		{"bug表", []string{"标题", "严重级", "状态", "负责人", "发现版本"},
			func(t *store.FeishuTables, id string) { t.Bugs = id }},
		{"提测表", []string{"版本", "状态", "提测人", "测试负责人", "范围"},
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
