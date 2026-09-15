package model

type Member struct {
	ID        int64
	Name      string
	Type      string // human | agent
	Capacity  float64
	Notes     string
	CreatedAt string
	// 飞书成员表单向镜像元数据（真实源在本地的类型/容量，飞书侧只读查看）
	BitableRecordID   string
	BitableSyncedHash string
}

type Project struct {
	ID                             int64
	Key, Name, Description, Status string
	FeishuDocToken                 string
	FeishuBitableAppToken          string
	FeishuTaskTableID              string // 计划细化：spec 的单 table_id 拆为 task/version 两个
	FeishuVersionTableID           string
	CreatedAt                      string
}

type Task struct {
	ID                   int64
	ProjectID            int64
	Title, Description   string
	AssigneeID           int64 // 0 = 无人
	Status               string
	Priority             int
	EstimateDays         float64
	StartDate, DueDate   string
	VersionID            int64 // 0 = 无版本
	RequirementID        int64 // 0 = 无关联需求（需求拆解为任务，spec §3.1；本地引用，不参与 Bitable 同步）
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string // 本行上次与飞书收敛的时刻（UTC 文本，sync 覆盖警告的判定基准）
	Archived             bool
	StatusChangedAt      string
	CreatedAt, UpdatedAt string
}

type Version struct {
	ID                                 int64
	ProjectID                          int64
	Name                               string
	TargetDate                         string
	Status                             string // planned | in_dev | released | shipped
	Notes                              string
	BitableRecordID, BitableSyncedHash string
}

// ---- v1.1 研发交付闭环六实体（spec §3.1）----

type Requirement struct {
	ID                   int64
	ProjectID            int64
	Title, Description   string
	Status               string // proposed | reviewing | accepted | in_dev | delivered | rejected
	Priority             int
	OwnerID              int64 // 0 = 无人
	Source               string
	UID                  string // 全局身份（32 位十六进制，创建时生成；跨机引用按它解析，修复本地 id 撞号错链）
	FeishuDocToken       string
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type Review struct {
	ID                   int64
	ProjectID            int64
	RequirementID        int64  // 0 = 无关联需求
	Kind                 string // requirement | release | test
	HeldAt               string
	Conclusion           string // pending | passed | passed_with_notes | rejected
	FeishuDocToken       string
	CreatedBy            int64
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type Meeting struct {
	ID                   int64
	ProjectID            int64
	Title                string
	HeldAt               string
	FeishuDocToken       string
	CreatedBy            int64
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type Bug struct {
	ID                   int64
	ProjectID            int64
	Title, Description   string
	Severity             int    // 1..4 = P0..P3
	Status               string // open | fixing | fixed | verified | closed | wontfix
	ReporterID           int64
	AssigneeID           int64 // 0 = 无人
	RequirementID        int64 // 0 = 无
	FoundVersionID       int64 // 0 = 无
	FixTaskID            int64 // 0 = 无
	FeishuDocToken       string
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type TestSubmission struct {
	ID                   int64
	ProjectID            int64
	VersionID            int64
	RequirementID        int64 // 0 = 无
	SubmittedBy          int64
	TestOwnerID          int64  // 0 = 无人
	Status               string // draft | submitted | testing | passed | failed
	Scope                string
	FeishuDocToken       string
	SubmittedAt          string // 首次进入 submitted/testing 及之后时补记（draft 为空）
	ConcludedAt          string // 进入 passed/failed 的时刻
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type Release struct {
	ID                   int64
	ProjectID            int64
	VersionID            int64
	Status               string // preparing | testing | released | rolled_back
	ReleaseManagerID     int64
	ReleasedAt           string // 进入 released 的时刻
	FeishuDocToken       string
	Notes                string
	BitableRecordID      string
	BitableSyncedHash    string
	SyncedAt             string
	Archived             bool
	CreatedAt, UpdatedAt string
}

type Dependency struct {
	ID, TaskID, DependsOnTaskID int64
	Type                        string
}

type Activity struct {
	ID         int64
	ProjectID  int64
	ActorID    int64
	ActorType  string
	OnBehalfOf int64 // 0 = 无
	Action     string
	EntityType string
	EntityID   int64
	Detail     string // JSON
	CreatedAt  string
}
