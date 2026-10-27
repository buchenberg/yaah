package tools

import (
	"context"
	"strings"
	"testing"
)

// TestParseDiffStats_countsIndentedContent pins yaah-dz0: content lines that
// begin with a space render as "+ "/"- " in the diff and must still count.
func TestParseDiffStats_countsIndentedContent(t *testing.T) {
	diff := `diff --git a/config.yaml b/config.yaml
index 1111111..2222222 100644
--- a/config.yaml
+++ b/config.yaml
@@ -1,3 +1,3 @@
 key:
-  old
+  new
 unchanged
`
	files, ins, del := parseDiffStats(diff)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Path != "config.yaml" {
		t.Errorf("path = %q, want config.yaml", files[0].Path)
	}
	if files[0].Insertions != 1 || files[0].Deletions != 1 {
		t.Errorf("file stats = %+v, want 1 insertion / 1 deletion", files[0])
	}
	if ins != 1 || del != 1 {
		t.Errorf("totals = %d/%d, want 1/1", ins, del)
	}
}

func TestParseDiffStats_multipleFiles(t *testing.T) {
	diff := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old-a
+new-a
diff --git a/b.txt b/b.txt
--- a/b.txt
+++ b/b.txt
@@ -1,2 +1,2 @@
 keep
-old-b
+new-b
`
	files, ins, del := parseDiffStats(diff)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].Path != "a.txt" || files[1].Path != "b.txt" {
		t.Errorf("paths = %q, %q", files[0].Path, files[1].Path)
	}
	if ins != 2 || del != 2 {
		t.Errorf("totals = %d/%d, want 2/2", ins, del)
	}
}

func TestParseDiffStats_newFile(t *testing.T) {
	diff := `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+one
+two
`
	files, ins, _ := parseDiffStats(diff)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Path != "new.txt" {
		t.Errorf("path = %q, want new.txt", files[0].Path)
	}
	if !files[0].IsNew {
		t.Error("new file not marked IsNew")
	}
	if ins != 2 {
		t.Errorf("insertions = %d, want 2", ins)
	}
}

func TestParseDiffStats_deletedFile(t *testing.T) {
	diff := `diff --git a/old.txt b/old.txt
deleted file mode 100644
index 1111111..0000000
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
`
	files, _, del := parseDiffStats(diff)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Path != "old.txt" {
		t.Errorf("path = %q, want old.txt", files[0].Path)
	}
	if !files[0].IsDeleted {
		t.Error("deleted file not marked IsDeleted")
	}
	if del != 2 {
		t.Errorf("deletions = %d, want 2", del)
	}
}

func TestDiffTool_invalidModeIsAnError(t *testing.T) {
	_, err := (&DiffTool{}).Execute(context.Background(), `{"a":"HEAD","mode":"bogus"}`)
	if err == nil {
		t.Fatal("an unknown mode must be an argument error")
	}
	if !strings.Contains(err.Error(), "invalid mode") || !strings.Contains(err.Error(), "git, unified") {
		t.Errorf("error should cite the allowed modes: %v", err)
	}
}
