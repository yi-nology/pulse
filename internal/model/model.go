package model

type Member struct {
	ID        int64
	Name      string
	Type      string // human | agent
	Capacity  float64
	Notes     string
	CreatedAt string
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
	BitableRecordID      string
	BitableSyncedHash    string
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
