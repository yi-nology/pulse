package feishu

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// callRec 记录 fakeAPI 的一次方法调用：方法名 + 字符串参数 + TableCreate 的字段定义。
type callRec struct {
	method string
	args   []string
	fields []Field // 仅 TableCreate 使用
}

// fakeAPI 是 bind 的测试替身：按序记录调用，可注入失败；仅实现 bitableAPI。
type fakeAPI struct {
	calls   []callRec
	appErr  error // AppCreate 注入失败
	viewErr error // ViewCreate 注入失败（bind 应仅警告不中断）
}

func (f *fakeAPI) record(method string, args ...string) {
	f.calls = append(f.calls, callRec{method: method, args: args})
}

func (f *fakeAPI) AppCreate(ctx context.Context, name string) (string, error) {
	if f.appErr != nil {
		return "", f.appErr
	}
	f.record("AppCreate", name)
	return "appT", nil
}

func (f *fakeAPI) TableCreate(ctx context.Context, appToken, name string, fields []Field) (string, error) {
	f.record("TableCreate", appToken, name)
	id := "tblVer"
	if name == "任务表" {
		id = "tblTask"
	}
	f.calls[len(f.calls)-1].fields = fields
	return id, nil
}

func (f *fakeAPI) ViewCreate(ctx context.Context, appToken, tableID, name, viewType string) error {
	f.record("ViewCreate", appToken, tableID, name, viewType)
	return f.viewErr
}

func (f *fakeAPI) RecordSearch(ctx context.Context, appToken, tableID string) ([]Record, error) {
	f.record("RecordSearch", appToken, tableID)
	return nil, nil
}

func (f *fakeAPI) RecordCreate(ctx context.Context, appToken, tableID string, fields map[string]any) (string, error) {
	f.record("RecordCreate", appToken, tableID)
	return "rec1", nil
}

func (f *fakeAPI) RecordUpdate(ctx context.Context, appToken, tableID, recordID string, fields map[string]any) error {
	f.record("RecordUpdate", appToken, tableID, recordID)
	return nil
}

func (f *fakeAPI) DocCreate(ctx context.Context, folderToken, title string) (string, error) {
	f.record("DocCreate", folderToken, title)
	return "docT", nil
}

func (f *fakeAPI) BlockAppend(ctx context.Context, docToken string, blocks []map[string]any) error {
	f.record("BlockAppend", docToken)
	return nil
}

// clientWith 把 fakeAPI 注入 Client（bind 只依赖 bitableAPI，经 API() 取用）。
func clientWith(fake *fakeAPI) *Client {
	c := NewClient("app-id", "app-secret", "https://fake.invalid")
	c.api = fake
	return c
}

// openStore 打开内存库并建演示项目，供 SaveProject 写回断言。
func openStore(t *testing.T) (*store.Store, model.Project) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.CreateProject("demo", "演示项目", "")
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

// captureWarn 把包级警告输出换到 buffer，测试结束后恢复。
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := warnWriter
	warnWriter = buf
	t.Cleanup(func() { warnWriter = old })
	return buf
}

// wantFields 断言字段定义为 名字→类型编号 的精确序列。
func wantFields(t *testing.T, got []Field, want []Field) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("字段数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w.Name || got[i].Type != w.Type {
			t.Fatalf("字段[%d] = %s(type %d), want %s(type %d)", i, got[i].Name, got[i].Type, w.Name, w.Type)
		}
	}
}

// TestBindCreatesBaseTablesViewDocAndSavesTokens：bind 按序调用
// AppCreate→TableCreate×2→ViewCreate→DocCreate，并把全部 token 经 SaveProject 写回。
func TestBindCreatesBaseTablesViewDocAndSavesTokens(t *testing.T) {
	s, p := openStore(t)
	fake := &fakeAPI{}
	warn := captureWarn(t)

	got, err := Bind(context.Background(), clientWith(fake), s, p)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// 调用序列与参数
	wantSeq := []string{"AppCreate", "TableCreate", "TableCreate", "ViewCreate", "DocCreate"}
	if len(fake.calls) != len(wantSeq) {
		t.Fatalf("调用数 = %d, want %d: %+v", len(fake.calls), len(wantSeq), fake.calls)
	}
	for i, m := range wantSeq {
		if fake.calls[i].method != m {
			t.Fatalf("调用[%d] = %s, want %s（完整序列 %+v）", i, fake.calls[i].method, m, fake.calls)
		}
	}
	if fake.calls[0].args[0] != "演示项目" {
		t.Fatalf("base 名 = %q, want 项目名", fake.calls[0].args[0])
	}
	// 任务表字段（brief §6 映射：日期/单选一律 text，预估人日 number，已废弃 checkbox）
	wantFields(t, fake.calls[1].fields, []Field{
		{Name: "任务名", Type: 1}, {Name: "状态", Type: 1}, {Name: "负责人", Type: 1},
		{Name: "优先级", Type: 1}, {Name: "开始", Type: 1}, {Name: "截止", Type: 1},
		{Name: "预估人日", Type: 2}, {Name: "版本", Type: 1},
		{Name: "已废弃", Type: 7}, {Name: "updated_by", Type: 1},
	})
	if fake.calls[1].args[0] != "appT" || fake.calls[1].args[1] != "任务表" {
		t.Fatalf("任务表建表参数 = %+v", fake.calls[1].args)
	}
	// 版本表字段
	wantFields(t, fake.calls[2].fields, []Field{
		{Name: "版本名", Type: 1}, {Name: "目标日期", Type: 1},
		{Name: "状态", Type: 1}, {Name: "备注", Type: 1},
	})
	// 甘特视图建在任务表上
	if got := fake.calls[3].args; got[0] != "appT" || got[1] != "tblTask" || got[2] != "甘特" || got[3] != "gantt" {
		t.Fatalf("ViewCreate 参数 = %+v, want appT/tblTask/甘特/gantt", got)
	}
	// 文档建在根目录，标题含项目名
	if got := fake.calls[4].args; got[0] != "" || !strings.Contains(got[1], "演示项目") {
		t.Fatalf("DocCreate 参数 = %+v, want 根目录 + 含项目名标题", got)
	}

	// 返回值与 store 落库的 token 完整一致
	wantP := p
	wantP.FeishuBitableAppToken = "appT"
	wantP.FeishuTaskTableID = "tblTask"
	wantP.FeishuVersionTableID = "tblVer"
	wantP.FeishuDocToken = "docT"
	if got != wantP {
		t.Fatalf("Bind 返回 = %+v, want %+v", got, wantP)
	}
	saved, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("reload project: found=%v err=%v", found, err)
	}
	if saved != wantP {
		t.Fatalf("SaveProject 落库 = %+v, want %+v", saved, wantP)
	}
	if warn.Len() != 0 {
		t.Fatalf("正常路径不应有警告: %q", warn.String())
	}
}

// TestBindIdempotent：项目已有 bitable_app_token 时再 Bind 直接返回，不产生任何调用、不改库。
func TestBindIdempotent(t *testing.T) {
	s, p := openStore(t)
	fake := &fakeAPI{}
	c := clientWith(fake)

	bound, err := Bind(context.Background(), c, s, p)
	if err != nil {
		t.Fatalf("首次 Bind: %v", err)
	}
	n := len(fake.calls)

	again, err := Bind(context.Background(), c, s, bound)
	if err != nil {
		t.Fatalf("二次 Bind: %v", err)
	}
	if len(fake.calls) != n {
		t.Fatalf("二次 Bind 产生了新调用: %+v", fake.calls[n:])
	}
	if again != bound {
		t.Fatalf("二次 Bind 返回 = %+v, want 原样返回 %+v", again, bound)
	}
}

// TestBindViewCreateFailureWarnsOnly：甘特视图创建失败仅警告，bind 整体成功且 token 照常写回。
func TestBindViewCreateFailureWarnsOnly(t *testing.T) {
	s, p := openStore(t)
	fake := &fakeAPI{viewErr: errors.New("gantt 视图类型不可用")}
	warn := captureWarn(t)

	got, err := Bind(context.Background(), clientWith(fake), s, p)
	if err != nil {
		t.Fatalf("ViewCreate 失败不应中断 bind: %v", err)
	}
	for i, m := range []string{"AppCreate", "TableCreate", "TableCreate", "ViewCreate", "DocCreate"} {
		if fake.calls[i].method != m {
			t.Fatalf("调用[%d] = %s, want %s", i, fake.calls[i].method, m)
		}
	}
	if !strings.Contains(warn.String(), "甘特") {
		t.Fatalf("警告应提及甘特视图, got %q", warn.String())
	}
	saved, _, err := s.GetProjectByKey("demo")
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuBitableAppToken != "appT" || saved.FeishuTaskTableID != "tblTask" ||
		saved.FeishuVersionTableID != "tblVer" || saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未完整写回: %+v", saved)
	}
	if got.FeishuDocToken != "docT" {
		t.Fatalf("返回项目 DocToken = %q, want docT", got.FeishuDocToken)
	}
}

// TestBindAppCreateErrorAborts：AppCreate 失败时立即返回错误，不做后续调用、不写库。
func TestBindAppCreateErrorAborts(t *testing.T) {
	s, p := openStore(t)
	fake := &fakeAPI{appErr: errors.New("无权限")}
	captureWarn(t)

	if _, err := Bind(context.Background(), clientWith(fake), s, p); err == nil {
		t.Fatal("AppCreate 失败必须返回错误")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("失败路径不应继续调用: %+v", fake.calls)
	}
	saved, _, err := s.GetProjectByKey("demo")
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuBitableAppToken != "" {
		t.Fatalf("失败路径不应写回 token: %+v", saved)
	}
}
