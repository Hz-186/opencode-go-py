package baseline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateFeatureMatrixProducesStableTypedMirror(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "PLAN.md")
	plan := strings.Join([]string{
		"# Plan",
		"",
		"| 功能 | OpenCode 源码位置 | 当前行为 | 依赖 | Go/Python 归属 | 阶段 | 测试依据 | 难度 | 状态 |",
		"|---|---|---|---|---|---|---|---|---|",
		"| Durable Event | `core/event.ts` | commit 后通知 | SQLite | Go | P4 | `event.test.ts` | VH | pending |",
		`| Wiki \| Web | MCP boundary | [replica-extension] 可选连接器 | MCP | Python 可选 | P7 | contract fixture | H | in_progress |`,
		"",
		"## Next",
	}, "\r\n")
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatalf("write plan fixture: %v", err)
	}

	first, err := GenerateFeatureMatrix(FeatureMatrixOptions{PlanPath: planPath, SourcePath: "doc/PLAN.md"})
	if err != nil {
		t.Fatalf("generate feature matrix: %v", err)
	}
	second, err := GenerateFeatureMatrix(FeatureMatrixOptions{PlanPath: planPath, SourcePath: "doc/PLAN.md"})
	if err != nil {
		t.Fatalf("regenerate feature matrix: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("feature matrix output is not deterministic")
	}

	var matrix FeatureMatrix
	if err := json.Unmarshal(first.JSON, &matrix); err != nil {
		t.Fatalf("decode feature matrix: %v", err)
	}
	if matrix.SchemaVersion != 1 || matrix.Source.Path != "doc/PLAN.md" {
		t.Fatalf("matrix header = %#v", matrix)
	}
	if len(matrix.Features) != 2 {
		t.Fatalf("features = %d, want 2", len(matrix.Features))
	}
	if !strings.HasPrefix(matrix.Features[0].ID, "FM-") || matrix.Features[0].Classification != FeatureCanonical {
		t.Fatalf("canonical feature = %#v", matrix.Features[0])
	}
	if matrix.Features[1].Name != "Wiki | Web" || matrix.Features[1].Classification != FeatureReplicaExtension {
		t.Fatalf("extension feature = %#v", matrix.Features[1])
	}
	if matrix.Features[1].Status != StatusInProgress {
		t.Fatalf("feature status = %q, want %q", matrix.Features[1].Status, StatusInProgress)
	}
}

func TestGenerateFeatureMatrixRejectsDuplicateFeature(t *testing.T) {
	planPath := writeMatrixFixture(t, "pending", "pending", "Same", "Same")

	_, err := GenerateFeatureMatrix(FeatureMatrixOptions{PlanPath: planPath, SourcePath: "PLAN.md"})
	if !errors.Is(err, ErrDuplicateFeature) {
		t.Fatalf("error = %v, want ErrDuplicateFeature", err)
	}
}

func TestGenerateFeatureMatrixRejectsUnknownStatus(t *testing.T) {
	planPath := writeMatrixFixture(t, "done", "pending", "One", "Two")

	_, err := GenerateFeatureMatrix(FeatureMatrixOptions{PlanPath: planPath, SourcePath: "PLAN.md"})
	if !errors.Is(err, ErrFeatureStatus) {
		t.Fatalf("error = %v, want ErrFeatureStatus", err)
	}
}

func TestProjectFeatureMatrixHasExpectedFrozenInventory(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	result, err := GenerateFeatureMatrix(FeatureMatrixOptions{
		PlanPath:   filepath.Join(root, "doc", "OPENCODE_REPLICA_MASTER_PLAN.md"),
		SourcePath: "doc/OPENCODE_REPLICA_MASTER_PLAN.md",
	})
	if err != nil {
		t.Fatalf("generate project feature matrix: %v", err)
	}
	if len(result.Matrix.Features) != 65 {
		t.Fatalf("feature count = %d, want 65", len(result.Matrix.Features))
	}
}

func writeMatrixFixture(t *testing.T, firstStatus string, secondStatus string, firstName string, secondName string) string {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "PLAN.md")
	content := "| 功能 | OpenCode 源码位置 | 当前行为 | 依赖 | Go/Python 归属 | 阶段 | 测试依据 | 难度 | 状态 |\n" +
		"|---|---|---|---|---|---|---|---|---|\n" +
		"| " + firstName + " | source | behavior | dependency | Go | P0 | test | M | " + firstStatus + " |\n" +
		"| " + secondName + " | source | behavior | dependency | Go | P0 | test | M | " + secondStatus + " |\n"
	if err := os.WriteFile(planPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write matrix fixture: %v", err)
	}
	return planPath
}
