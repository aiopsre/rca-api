package main

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/onexstack/onexstack/pkg/db"
	"github.com/spf13/pflag"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

// 帮助信息文本.
const helpText = `Usage: main [flags] arg [arg...]

This is a pflag example.

Flags:
`

// Querier 定义了数据库查询接口.
type Querier interface {
	// FilterWithNameAndRole 按名称和角色查询记录
	FilterWithNameAndRole(name string) ([]gen.T, error)
}

// GenerateConfig 保存代码生成的配置.
type GenerateConfig struct {
	ModelPackagePath string
	GenerateFunc     func(g *gen.Generator)
}

// 预定义的生成配置.
var generateConfigs = map[string]GenerateConfig{
	"rca": {ModelPackagePath: "./internal/apiserver/model", GenerateFunc: GenerateRCAModels},
}

// 命令行参数.
var (
	addr       = pflag.StringP("addr", "a", "192.168.39.1:3306", "MySQL host address.")
	username   = pflag.StringP("username", "u", "root", "Username to connect to the database.")
	password   = pflag.StringP("password", "p", "Az123456_", "Password to use when connecting to the database.")
	database   = pflag.StringP("db", "d", "rca", "Database name to connect to.")
	modelPath  = pflag.String("model-pkg-path", "", "Generated model code's package name.")
	components = pflag.StringSlice("component", []string{"rca"}, "Generated model code's for specified component.")
	help       = pflag.BoolP("help", "h", false, "Show this help message.")
)

func main() {
	// 设置自定义的使用说明函数
	pflag.Usage = func() {
		fmt.Printf("%s", helpText)
		pflag.PrintDefaults()
	}
	pflag.Parse()

	// 如果设置了帮助标志，则显示帮助信息并退出
	if *help {
		pflag.Usage()
		return
	}

	// 初始化数据库连接
	dbInstance, err := initializeDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 处理组件并生成代码
	for _, component := range *components {
		processComponent(component, dbInstance)
	}
}

// initializeDatabase 创建并返回一个数据库连接.
func initializeDatabase() (*gorm.DB, error) {
	dbOptions := &db.MySQLOptions{
		Addr:     *addr,
		Username: *username,
		Password: *password,
		Database: *database,
	}

	// 创建并返回数据库连接
	return db.NewMySQL(dbOptions)
}

// processComponent 处理单个组件以生成代码.
func processComponent(component string, dbInstance *gorm.DB) {
	config, ok := generateConfigs[component]
	if !ok {
		log.Printf("Component '%s' not found in configuration. Skipping.", component)
		return
	}

	// 解析模型包路径
	modelPkgPath := resolveModelPackagePath(config.ModelPackagePath)

	// 创建生成器实例
	generator := createGenerator(modelPkgPath)
	generator.UseDB(dbInstance)

	// 应用自定义生成器选项
	applyGeneratorOptions(generator)

	// 使用指定的函数生成模型
	config.GenerateFunc(generator)

	// 执行代码生成
	generator.Execute()
}

// resolveModelPackagePath 确定模型生成的包路径.
func resolveModelPackagePath(defaultPath string) string {
	if *modelPath != "" {
		return *modelPath
	}
	absPath, err := filepath.Abs(defaultPath)
	if err != nil {
		log.Printf("Error resolving path: %v", err)
		return defaultPath
	}
	return absPath
}

// createGenerator 初始化并返回一个新的生成器实例.
func createGenerator(packagePath string) *gen.Generator {
	return gen.NewGenerator(gen.Config{
		Mode:              gen.WithDefaultQuery | gen.WithQueryInterface | gen.WithoutContext,
		ModelPkgPath:      packagePath,
		WithUnitTest:      true,
		FieldNullable:     true,  // 对于数据库中可空的字段，使用指针类型。
		FieldSignable:     false, // 禁用无符号属性以提高兼容性。
		FieldWithIndexTag: false, // 不包含 GORM 的索引标签。
		FieldWithTypeTag:  false, // 不包含 GORM 的类型标签。
	})
}

// applyGeneratorOptions 设置自定义生成器选项.
func applyGeneratorOptions(g *gen.Generator) {
	// 为特定字段自定义 GORM 标签
	g.WithOpts(
		gen.FieldGORMTag("createdAt", func(tag field.GormTag) field.GormTag {
			tag.Set("default", "current_timestamp")
			return tag
		}),
		gen.FieldGORMTag("updatedAt", func(tag field.GormTag) field.GormTag {
			tag.Set("default", "current_timestamp")
			return tag
		}),
	)
}

// GenerateRCAModels 为 rca-api 组件生成 gorm/gen 模型。
// 约定：数据库表为 DB-first（先建表），gen 只做反向生成。
func GenerateRCAModels(g *gen.Generator) {
	// incidents -> IncidentM
	g.GenerateModelAs(
		"incidents",
		"IncidentM",

		// --- 常见列名重命名：生成出来的 Go 字段更符合习惯 ---
		gen.FieldRename("id", "ID"),
		gen.FieldRename("incident_id", "IncidentID"),
		gen.FieldRename("tenant_id", "TenantID"),
		gen.FieldRename("cluster", "Cluster"),
		gen.FieldRename("namespace", "Namespace"),
		gen.FieldRename("workload_kind", "WorkloadKind"),
		gen.FieldRename("workload_name", "WorkloadName"),
		gen.FieldRename("pod", "Pod"),
		gen.FieldRename("node", "Node"),
		gen.FieldRename("service", "Service"),
		gen.FieldRename("environment", "Environment"),
		gen.FieldRename("version", "Version"),

		gen.FieldRename("source", "Source"),
		gen.FieldRename("alertname", "AlertName"),
		gen.FieldRename("fingerprint", "Fingerprint"),
		gen.FieldRename("rule_id", "RuleID"),
		gen.FieldRename("labels_json", "LabelsJSON"),
		gen.FieldRename("annotations_json", "AnnotationsJSON"),

		gen.FieldRename("severity", "Severity"),
		gen.FieldRename("status", "Status"),
		gen.FieldRename("start_at", "StartAt"),
		gen.FieldRename("end_at", "EndAt"),

		gen.FieldRename("rca_status", "RCAStatus"),
		gen.FieldRename("root_cause_type", "RootCauseType"),
		gen.FieldRename("root_cause_summary", "RootCauseSummary"),
		gen.FieldRename("diagnosis_json", "DiagnosisJSON"),
		gen.FieldRename("evidence_refs_json", "EvidenceRefsJSON"),

		gen.FieldRename("action_status", "ActionStatus"),
		gen.FieldRename("action_summary", "ActionSummary"),

		gen.FieldRename("trace_id", "TraceID"),
		gen.FieldRename("log_trace_key", "LogTraceKey"),
		gen.FieldRename("change_id", "ChangeID"),

		gen.FieldRename("created_by", "CreatedBy"),
		gen.FieldRename("approved_by", "ApprovedBy"),
		gen.FieldRename("closed_by", "ClosedBy"),

		gen.FieldRename("created_at", "CreatedAt"),
		gen.FieldRename("updated_at", "UpdatedAt"),

		// --- 如果你建表里有 JSON 列，gen 会按驱动与版本生成类型 ---
		// 一般会是 []byte 或 datatypes.JSON；这里不强制改类型，保持与 gen 输出一致即可

		// --- 如需忽略某些列（比如 placeholder），可用 FieldIgnore ---
		gen.FieldIgnore("placeholder"),

		// --- 可选：给某些列补充 uniqueIndex/tag（你已在 SQL 建索引，这里通常不需要） ---
		//gen.FieldGORMTag("incident_id", func(tag field.GormTag) field.GormTag {
		//	tag.Set("uniqueIndex", "idx_incident_id")
		//	return tag
		//}),
	)

	// 未来扩展：evidences/actions 表（你建好表后再打开）
	// g.GenerateModelAs("evidences", "EvidenceM", gen.FieldRename("id", "ID"))
	// g.GenerateModelAs("actions", "ActionM", gen.FieldRename("id", "ID"))
}

// GenerateMiniblogModels 为 miniblog 组件生成模型.
func GenerateMiniblogModels(g *gen.Generator) {
	g.GenerateModelAs(
		"user",
		"UserM",
		gen.FieldIgnore("placeholder"),
		gen.FieldGORMTag("username", func(tag field.GormTag) field.GormTag {
			tag.Set("uniqueIndex", "idx_user_username")
			return tag
		}),
		gen.FieldGORMTag("userID", func(tag field.GormTag) field.GormTag {
			tag.Set("uniqueIndex", "idx_user_userID")
			return tag
		}),
		gen.FieldGORMTag("phone", func(tag field.GormTag) field.GormTag {
			tag.Set("uniqueIndex", "idx_user_phone")
			return tag
		}),
	)
	g.GenerateModelAs(
		"post",
		"PostM",
		gen.FieldIgnore("placeholder"),
		gen.FieldGORMTag("postID", func(tag field.GormTag) field.GormTag {
			tag.Set("uniqueIndex", "idx_post_postID")
			return tag
		}),
	)
	g.GenerateModelAs(
		"casbin_rule",
		"CasbinRuleM",
		gen.FieldRename("ptype", "PType"),
		gen.FieldIgnore("placeholder"),
	)
}
