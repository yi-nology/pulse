package feishu

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// warnWriter 输出非致命警告（如甘特视图创建失败）；包级变量便于测试注入捕获。
var warnWriter io.Writer = os.Stderr

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

// Bind 为项目创建共享 base（任务表+版本表+甘特视图）与沉淀文档，token 经 SaveProject 写回
// 项目记录。幂等：项目已有 bitable_app_token 时直接原样返回，零 API 调用、零写库。
// 调用序列：AppCreate(项目名) → TableCreate(任务表) → TableCreate(版本表) →
// ViewCreate(甘特, 失败仅警告不中断) → DocCreate → SaveProject。
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
	docToken, err := api.DocCreate(ctx, "", p.Name+" 沉淀文档")
	if err != nil {
		return model.Project{}, fmt.Errorf("创建沉淀文档失败: %w", err)
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
