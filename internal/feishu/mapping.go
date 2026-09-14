// mapping.go 定义本地实体与 Bitable 字段的双向映射与内容指纹。
// 映射刻意保持"往返一致"（TaskToFields ∘ FieldsToTask = id）：自回声判定依赖
// 「push 时对本地映射字段算 hash」与「pull 时对远端字段经映射再算 hash」相等，
// 故两端必须产出完全相同的键集合与 JSON 类型（文本列一律 string、数字列 float64、
// 复选框 bool）。远端缺必填字段 → missing 非空（整条跳过并告警，spec §8 缺字段告警）；
// 远端出现未知字段 → 忽略并计入 warnings（有人在飞书手改表结构）。
package feishu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/zhangyi/pulse/internal/model"
)

// DeprecatedHash 是 archived 行 bitable_synced_hash 的终态值：墓碑已推送，
// 之后不再重推（本地软删的不可逆终点）。
const DeprecatedHash = "deprecated"

// ContentHash 对字段映射做规范化（键排序的 canonical JSON）后取 sha256 前 16 位十六进制。
// encoding/json 对 map 键按字典序输出，天然满足 canonical；int 与等值 float64 的 JSON
// 形态相同（如 3 与 3.0 均为 "3"），故类型微差不影响指纹。
func ContentHash(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil { // 字段值均为 string/float64/bool，理论不可达；兜底给稳定值
		b = []byte(fmt.Sprintf("%v", fields))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// TaskToFields 把本地任务映射为 Bitable 字段（与 bind 建表列对应；updated_by 列
// v1.0 不参与同步，不计入指纹）。memberNameByID/versionNameByID 缺项时对应列落空串。
func TaskToFields(t model.Task, memberNameByID map[int64]string, versionNameByID map[int64]string) map[string]any {
	return map[string]any{
		"任务名":  t.Title,
		"状态":   t.Status,
		"负责人":  memberNameByID[t.AssigneeID],
		"优先级":  strconv.Itoa(t.Priority),
		"开始":   t.StartDate,
		"截止":   t.DueDate,
		"预估人日": t.EstimateDays,
		"版本":   versionNameByID[t.VersionID],
		"已废弃":  t.Archived,
	}
}

// taskRequiredFields 是 pull 侧缺一即跳过整条记录的必填列（其余列可被清空为缺省，
// Bitable 清空单元格后该键在 search 结果中缺失，属正常形态而非表结构损坏）。
var taskRequiredFields = []string{"任务名", "状态", "优先级", "预估人日"}

// taskIgnoredFields 是 pulse 自己创建、但 v1.0 不参与同步的列：出现时静默忽略，不告警。
var taskIgnoredFields = map[string]bool{"updated_by": true}

// FieldsToTask 把远端字段映射到本地任务的变更（local 提供未映射/缺省字段的底值，
// 如 Description 本地独有、Archived 不被远端内容覆盖）。
// 返回：变更后的任务、缺失的必填字段、未知字段告警。负责人按 memberIDByName 解析，
// 查不到时保留本地负责人并告警（sync.go 会先 get-or-create 远端提到的成员名）；
// 版本按 versionIDByName 解析，本地无同名版本时保留原版本并告警。
func FieldsToTask(f map[string]any, local model.Task, memberIDByName map[string]int64, versionIDByName map[string]int64) (changed model.Task, missing []string, warnings []string) {
	changed = local
	for _, k := range taskRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Title = toText(f["任务名"])
	changed.Status = toText(f["状态"])
	if v, ok := f["负责人"]; ok {
		name := toText(v)
		if name == "" {
			changed.AssigneeID = 0
		} else if id, ok := memberIDByName[name]; ok {
			changed.AssigneeID = id
		} else {
			warnings = append(warnings, fmt.Sprintf("负责人 %q 不在本地成员表，保留原负责人", name))
		}
	}
	if v, ok := f["优先级"]; ok {
		if n, err := strconv.Atoi(toText(v)); err == nil {
			changed.Priority = n
		} else {
			warnings = append(warnings, fmt.Sprintf("优先级 %q 无法解析为整数，保留原值", toText(v)))
		}
	}
	if v, ok := f["开始"]; ok {
		changed.StartDate = toText(v)
	}
	if v, ok := f["截止"]; ok {
		changed.DueDate = toText(v)
	}
	if v, ok := f["预估人日"]; ok {
		changed.EstimateDays = toFloat(v)
	}
	if v, ok := f["版本"]; ok {
		vname := toText(v)
		if vname == "" {
			changed.VersionID = 0
		} else if vid, ok := versionIDByName[vname]; ok {
			changed.VersionID = vid
		} else {
			warnings = append(warnings, fmt.Sprintf("版本 %q 不在本地版本表，保留原版本", vname))
		}
	}
	for k := range f {
		if _, mapped := taskFieldSet[k]; !mapped && !taskIgnoredFields[k] {
			warnings = append(warnings, fmt.Sprintf("未知字段 %q 已忽略（可能有人在 Bitable 手改了表结构）", k))
		}
	}
	return changed, nil, warnings
}

// taskFieldSet 是 TaskToFields 产出的键集合，供未知字段识别。
var taskFieldSet = map[string]bool{
	"任务名": true, "状态": true, "负责人": true, "优先级": true, "开始": true,
	"截止": true, "预估人日": true, "版本": true, "已废弃": true,
}

// VersionToFields 把本地版本映射为 Bitable 字段（版本表无已废弃列：版本无软删语义）。
func VersionToFields(v model.Version) map[string]any {
	return map[string]any{
		"版本名":  v.Name,
		"目标日期": v.TargetDate,
		"状态":   v.Status,
		"备注":   v.Notes,
	}
}

// versionRequiredFields 是 pull 侧版本记录的必填列。
var versionRequiredFields = []string{"版本名"}

// FieldsToVersion 把远端字段映射到本地版本的变更（版本名是身份列，不允许被远端改名——
// 名字不同视为异常数据，由调用方按 missing 处理语义跳过并告警）。
func FieldsToVersion(f map[string]any, local model.Version) (changed model.Version, missing []string, warnings []string) {
	changed = local
	for _, k := range versionRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Name = toText(f["版本名"])
	if v, ok := f["目标日期"]; ok {
		changed.TargetDate = toText(v)
	}
	if v, ok := f["状态"]; ok {
		changed.Status = toText(v)
	}
	if v, ok := f["备注"]; ok {
		changed.Notes = toText(v)
	}
	for k := range f {
		if _, mapped := versionFieldSet[k]; !mapped {
			warnings = append(warnings, fmt.Sprintf("未知字段 %q 已忽略（可能有人在 Bitable 手改了表结构）", k))
		}
	}
	return changed, nil, warnings
}

// versionFieldSet 是 VersionToFields 产出的键集合。
var versionFieldSet = map[string]bool{"版本名": true, "目标日期": true, "状态": true, "备注": true}

// —— 远端值的宽松取值助手（Bitable 各列类型的回传形态可能随端点微差）—————————————————

// toText 把远端标量转为文本：string 原样；数字/布尔按 JSON 形态；其余 fmt 兜底。
func toText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// toFloat 把远端数值转为 float64：数字直取；字符串尽力解析；其余 0。
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// truthy 判断已废弃列是否为真：bool 直取；字符串 "true"/"1"/"是"；非零数字。
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "是"
	case float64:
		return x != 0
	default:
		return false
	}
}
