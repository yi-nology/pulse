// Package feishu 是飞书开放平台的 Minimal 客户端基座：tenant_access_token 获取/缓存/过期前刷新、
// 限流（429/99991400）指数退避重试、按 base（appToken）串行写，并把全部飞书 HTTP 交互
// 收口到 bitableAPI 接口——bind/sync/publish 只依赖该接口，测试注入 fake 实现或 httptest 地址。
// Client 基座零 internal/store 依赖；bind 等上层能力经 store.SaveProject 写回 token（见 bind.go）。
// 本包不含任何 LLM 逻辑。
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// —— 对外契约（接口与数据结构）——————————————————————————

// bitableAPI 收口全部飞书 HTTP 交互；上层只依赖该接口，测试用 fake 实现替换。
type bitableAPI interface {
	AppCreate(ctx context.Context, name string) (appToken string, err error)
	TableCreate(ctx context.Context, appToken, name string, fields []Field) (tableID string, err error)
	ViewCreate(ctx context.Context, appToken, tableID, name, viewType string) error
	RecordSearch(ctx context.Context, appToken, tableID string) ([]Record, error) // 内部分页聚合
	RecordCreate(ctx context.Context, appToken, tableID string, fields map[string]any) (recordID string, err error)
	RecordUpdate(ctx context.Context, appToken, tableID, recordID string, fields map[string]any) error
	DocCreate(ctx context.Context, folderToken, title string) (docToken string, err error)
	BlockAppend(ctx context.Context, docToken string, blocks []map[string]any) error
}

// Record 是搜索结果里的一条记录；LastModifiedTime 统一为秒级 int64
// （飞书不同端点对 last_modified_time 可能回传字符串或数字，此处归一化）。
type Record struct {
	RecordID         string
	Fields           map[string]any
	LastModifiedTime int64 // 秒
}

// Field 描述多维表格字段定义；Type 为飞书字段类型编号（1 文本、2 数字、3 单选、4 多选、5 日期、15 超链接等）。
type Field struct {
	Name     string         `json:"field_name"`
	Type     int            `json:"type"`
	Property map[string]any `json:"property,omitempty"` // 部分类型需要属性，如单选的 options
}

// —— 生产实现（net/http + encoding/json 直连官方 REST）——————————————————
// 说明：按任务约定放弃 larksuite/oapi-sdk-go（类型名随版本漂移），直接对接官方 REST，
// bitableAPI 接口签名不变。

// remoteAPI 是 bitableAPI 的生产实现，全部请求经 Client 的 token/退避/串行写基础设施发出。
type remoteAPI struct {
	c *Client
}

// AppCreate 新建多维表格（base），返回 app_token。
func (r remoteAPI) AppCreate(ctx context.Context, name string) (appToken string, err error) {
	var out struct {
		App struct {
			AppToken string `json:"app_token"`
		} `json:"app"`
	}
	if err := r.c.callAPI(ctx, http.MethodPost, "/open-apis/bitable/v1/apps",
		map[string]any{"name": name}, &out); err != nil {
		return "", err
	}
	return out.App.AppToken, nil
}

// TableCreate 在 base 内新建数据表并按 fields 建列，返回 table_id。
func (r remoteAPI) TableCreate(ctx context.Context, appToken, name string, fields []Field) (tableID string, err error) {
	var out struct {
		TableID string `json:"table_id"`
	}
	payload := map[string]any{
		"table": map[string]any{"name": name, "fields": fields},
	}
	path := fmt.Sprintf("/open-apis/bitable/v1/apps/%s/tables", url.PathEscape(appToken))
	if err := r.c.callAPI(ctx, http.MethodPost, path, payload, &out); err != nil {
		return "", err
	}
	return out.TableID, nil
}

// ViewCreate 在数据表内新建视图（viewType 如 grid/kanban/gallery/form/gantt）。
func (r remoteAPI) ViewCreate(ctx context.Context, appToken, tableID, name, viewType string) error {
	payload := map[string]any{"view_name": name, "view_type": viewType}
	path := fmt.Sprintf("/open-apis/bitable/v1/apps/%s/tables/%s/views",
		url.PathEscape(appToken), url.PathEscape(tableID))
	return r.c.callAPI(ctx, http.MethodPost, path, payload, nil)
}

// rawRecord 是搜索接口回传的记录原始形态；last_modified_time 用 RawMessage 接住以便归一化。
type rawRecord struct {
	RecordID         string          `json:"record_id"`
	Fields           map[string]any  `json:"fields"`
	LastModifiedTime json.RawMessage `json:"last_modified_time"`
}

// searchPage 是记录搜索单页响应。
type searchPage struct {
	Items     []rawRecord `json:"items"`
	HasMore   bool        `json:"has_more"`
	PageToken string      `json:"page_token"`
}

// RecordSearch 搜索表内全部记录：内部按 page_token 翻页并聚合，把
// last_modified_time 的字符串/数字形态统一为 int64 秒。
func (r remoteAPI) RecordSearch(ctx context.Context, appToken, tableID string) ([]Record, error) {
	base := fmt.Sprintf("/open-apis/bitable/v1/apps/%s/tables/%s/records/search",
		url.PathEscape(appToken), url.PathEscape(tableID))
	var records []Record
	pageToken := ""
	for {
		q := url.Values{"page_size": []string{"500"}}
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var page searchPage
		if err := r.c.callAPI(ctx, http.MethodPost, base+"?"+q.Encode(), map[string]any{}, &page); err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			records = append(records, Record{
				RecordID:         raw.RecordID,
				Fields:           raw.Fields,
				LastModifiedTime: parseSeconds(raw.LastModifiedTime),
			})
		}
		if !page.HasMore || page.PageToken == "" {
			return records, nil
		}
		pageToken = page.PageToken
	}
}

// RecordCreate 在表内新建一条记录，返回 record_id；经 lockBase 按 base 串行写。
func (r remoteAPI) RecordCreate(ctx context.Context, appToken, tableID string, fields map[string]any) (recordID string, err error) {
	unlock := r.c.lockBase(appToken) // spec："每 base 串行写"
	defer unlock()
	var out struct {
		Record struct {
			RecordID string `json:"record_id"`
		} `json:"record"`
	}
	path := fmt.Sprintf("/open-apis/bitable/v1/apps/%s/tables/%s/records",
		url.PathEscape(appToken), url.PathEscape(tableID))
	if err := r.c.callAPI(ctx, http.MethodPost, path, map[string]any{"fields": fields}, &out); err != nil {
		return "", err
	}
	return out.Record.RecordID, nil
}

// RecordUpdate 更新一条记录的部分字段；经 lockBase 按 base 串行写。
func (r remoteAPI) RecordUpdate(ctx context.Context, appToken, tableID, recordID string, fields map[string]any) error {
	unlock := r.c.lockBase(appToken) // spec："每 base 串行写"
	defer unlock()
	path := fmt.Sprintf("/open-apis/bitable/v1/apps/%s/tables/%s/records/%s",
		url.PathEscape(appToken), url.PathEscape(tableID), url.PathEscape(recordID))
	return r.c.callAPI(ctx, http.MethodPut, path, map[string]any{"fields": fields}, nil)
}

// DocCreate 在 folderToken 指定目录（空串表示根目录"我的空间"）新建一篇文档，返回 document_id。
func (r remoteAPI) DocCreate(ctx context.Context, folderToken, title string) (docToken string, err error) {
	var out struct {
		Document struct {
			DocumentID string `json:"document_id"`
		} `json:"document"`
	}
	payload := map[string]any{"title": title}
	if folderToken != "" {
		payload["folder_token"] = folderToken
	}
	if err := r.c.callAPI(ctx, http.MethodPost, "/open-apis/docx/v1/documents", payload, &out); err != nil {
		return "", err
	}
	return out.Document.DocumentID, nil
}

// BlockAppend 向文档末尾追加内容块（index=-1 表示追加到最后一个子块之后）。
// blockBatchSize 是 BlockAppend 单次请求追加的块数上限（真实租户实测大报表
// 一次性提交会 99992402 校验失败，分批最稳）。
const blockBatchSize = 40

func (r remoteAPI) BlockAppend(ctx context.Context, docToken string, blocks []map[string]any) error {
	// 文档根块的块 ID 即 document_id，故 path 中两处相同。
	path := fmt.Sprintf("/open-apis/docx/v1/documents/%s/blocks/%s/children",
		url.PathEscape(docToken), url.PathEscape(docToken))
	for start := 0; start < len(blocks); start += blockBatchSize {
		end := start + blockBatchSize
		if end > len(blocks) {
			end = len(blocks)
		}
		if err := r.c.callAPI(ctx, http.MethodPost, path,
			map[string]any{"index": -1, "children": blocks[start:end]}, nil); err != nil {
			return err
		}
	}
	return nil
}

// parseSeconds 把飞书回传的秒值统一成 int64：兼容裸数字、JSON 字符串、缺省/无法识别（返回 0）。
func parseSeconds(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil { // 裸数字形态
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil { // 字符串形态
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f)
	}
	return 0
}
