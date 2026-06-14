package guard

import (
	"os"
	"path/filepath"
	"testing"
)

const testVaultDir = "/Users/test/Library/Mobile Documents/iCloud~md~obsidian/Documents/Claude"
const testProjectRoot = "/Users/test/workspace/my-project"

func TestCheckFilePath_AllowsNonVaultPaths(t *testing.T) {
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: "/tmp/safe.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckFilePath_AllowsInbox(t *testing.T) {
	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: testVaultDir + "/_inbox/Proposal.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed for _inbox/, got blocked: %s", result.Reason)
	}
}

func TestCheckFilePath_BlocksProtectedDirs(t *testing.T) {
	for _, folder := range ProtectedFolders {
		t.Run(folder, func(t *testing.T) {
			input := HookInput{
				ToolName:  "Edit",
				ToolInput: ToolInput{FilePath: testVaultDir + "/" + folder + "/Some Note.md"},
			}
			result := Check(testVaultDir, testProjectRoot, input)
			if result.Allowed {
				t.Errorf("expected blocked for %s/, got allowed", folder)
			}
		})
	}
}

func TestCheckBash_AllowsVltCommands(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `vlt vault="Claude" append file="Developer Agent" content="test"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected vlt command allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_BlocksVltPipeToProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `vlt vault="Claude" read file="Developer Agent" | tee "` + testVaultDir + `/decisions/Hack.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected piped vlt command to protected dir blocked, got allowed")
	}
}

func TestCheckBash_BlocksVltRedirectToProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `vlt vault="Claude" read file="Developer Agent" > "` + testVaultDir + `/decisions/Hack.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected redirected vlt command to protected dir blocked, got allowed")
	}
}

func TestCheckBash_AllowsSafeCommands(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: "ls /tmp"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected safe command allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_BlocksRedirectToProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cat > "` + testVaultDir + `/methodology/Developer Agent.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for redirect to methodology/, got allowed")
	}
}

func TestCheckBash_BlocksCpToProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cp /tmp/new.md "` + testVaultDir + `/conventions/Session Operating Mode.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for cp to conventions/, got allowed")
	}
}

func TestCheckBash_BlocksRmFromProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `rm "` + testVaultDir + `/decisions/ADR-001.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for rm in decisions/, got allowed")
	}
}

func TestCheckBash_AllowsReadFromProtectedDir(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cat "` + testVaultDir + `/methodology/Developer Agent.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected read from vault allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsGrepWithProtectedDirInOutput(t *testing.T) {
	// A grep that reads from a protected dir should NOT be blocked.
	// The old guard would false-positive on this because ">" appears in the command
	// before the protected path (as part of the grep output).
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `grep "pattern" /tmp/file.txt > /tmp/output.txt`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected grep with redirect to non-vault path allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_BlocksSedInPlace(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `sed -i 's/old/new/' "` + testVaultDir + `/conventions/Mode.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for sed -i on conventions/, got allowed")
	}
}

func TestCheckBash_BlocksAppendRedirect(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `echo "new content" >> "` + testVaultDir + `/methodology/Agent.md"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for >> to methodology/, got allowed")
	}
}

func TestCheckFilePath_EmptyPath(t *testing.T) {
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: ""},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed for empty path, got blocked")
	}
}

func TestCheckUnknownTool_Allowed(t *testing.T) {
	input := HookInput{
		ToolName:  "Grep",
		ToolInput: ToolInput{},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected unknown tool allowed, got blocked")
	}
}

func TestCheckFilePath_BlocksTrailingDotDot(t *testing.T) {
	// Path traversal: go up from _inbox and back into protected dir.
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: testVaultDir + "/_inbox/../methodology/Hack.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for path traversal via .., got allowed")
	}
}

// --- Project vault tests ---

func TestCheckFilePath_BlocksProjectVault(t *testing.T) {
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/knowledge/decisions/test.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for .vault/knowledge/ edit, got allowed")
	}
	if result.Reason != projectVaultBlockMsg {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckFilePath_AllowsProjectVaultSettings(t *testing.T) {
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/knowledge/.settings.yaml"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed for .settings.yaml, got blocked: %s", result.Reason)
	}
}

func TestCheckFilePath_AllowsDispatcherStateOutsideKnowledge(t *testing.T) {
	// Dispatcher state lives in .vault/ (not .vault/knowledge/), so the
	// project vault guard should not block it.
	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/.dispatcher-state.json"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed for .vault/.dispatcher-state.json, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_BlocksProjectVaultWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{Command: `cat > ` + testProjectRoot + `/.vault/knowledge/patterns/test.md << 'EOF'
content
EOF`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for cat > .vault/knowledge/, got allowed")
	}
	if result.Reason != projectVaultBlockMsg {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckBash_AllowsProjectVaultVlt(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `vlt vault=".vault/knowledge" create name="test" path="patterns/test.md" content="..." silent`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected vlt command for project vault allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsProjectVaultGitAdd(t *testing.T) {
	// Staging a tracked .vault/knowledge file is the sanctioned dispatcher
	// path. Regression: "git aDD " contains "dd " which used to match the
	// dd(1) write-pattern and falsely block this command.
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `git add .vault/knowledge/conventions/testing.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected git add of tracked .vault/knowledge file allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsProjectVaultGitCommit(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `git commit -m "sync knowledge"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected git commit allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_BlocksGitRedirectIntoProjectVault(t *testing.T) {
	// A composed git command that pipes content INTO a vault file is not a
	// bare invocation and must stay blocked.
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `git show HEAD:doc.md > .vault/knowledge/conventions/hack.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for git redirect into .vault/knowledge/, got allowed")
	}
}

func TestCheckBash_BlocksDdWriteIntoProjectVault(t *testing.T) {
	// The precise dd(1) write-pattern must still catch a real dd write.
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `dd if=/dev/zero of=.vault/knowledge/conventions/test.md bs=1 count=1`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for dd of=.vault/knowledge/, got allowed")
	}
}

func TestCheckBash_BlocksProjectVaultRm(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `rm .vault/knowledge/patterns/test.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for rm in .vault/knowledge/, got allowed")
	}
}

func TestCheckBash_BlocksProjectVaultVltRedirect(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `vlt vault=".vault/knowledge" read file="patterns/test" > .vault/knowledge/patterns/hack.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for redirected vlt command into .vault/knowledge/, got allowed")
	}
}

func TestCheckBash_BlocksMixedVltAndProtectedWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{
			Command: `vlt vault="Claude" read file="Developer Agent"; rm "` + testVaultDir + `/methodology/Developer Agent.md"`,
		},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for mixed vlt + direct protected write, got allowed")
	}
}

func TestCheckBash_BlocksVltSubshellProtectedWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{
			Command: `vlt vault="Claude" read file="Developer Agent" $(rm "` + testVaultDir + `/methodology/Developer Agent.md")`,
		},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for vlt subshell direct protected write, got allowed")
	}
}

func TestCheckBash_BlocksMixedNDAndIssueWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{
			Command: `nd list --json; rm paivot/nd-vault/issues/PROJ-a1b.md`,
		},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for mixed nd + direct issue write, got allowed")
	}
}

func TestCheckBash_BlocksNDBacktickIssueWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{
			Command: "nd list --json `rm paivot/nd-vault/issues/PROJ-a1b.md`",
		},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for nd backtick direct issue write, got allowed")
	}
}

func TestCheckBash_BlocksProjectVaultSedInPlace(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `sed -i 's/old/new/' ` + testProjectRoot + `/.vault/knowledge/patterns/test.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for sed -i on .vault/knowledge/, got allowed")
	}
}

func TestCheckBash_BlocksRelativeProjectVaultWrite(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{Command: `cat > .vault/knowledge/patterns/test.md << 'EOF'
content
EOF`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for relative write to .vault/knowledge/, got allowed")
	}
}

func TestCheckFilePath_BlocksSharedProjectIssues(t *testing.T) {
	projectRoot, sharedVault := setupPaivotWorktree(t)
	issuePath := filepath.Join(sharedVault, "issues", "PROJ-a1b2.md")
	if err := os.MkdirAll(filepath.Dir(issuePath), 0755); err != nil {
		t.Fatal(err)
	}

	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: issuePath},
	}
	result := Check(testVaultDir, projectRoot, input)
	if result.Allowed {
		t.Fatal("expected shared nd-vault issue write to be blocked")
	}
	if result.Reason != projectIssuesBlockMsg {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckBash_BlocksSharedProjectIssues(t *testing.T) {
	projectRoot, sharedVault := setupPaivotWorktree(t)
	issuePath := filepath.Join(sharedVault, "issues", "PROJ-a1b2.md")
	if err := os.MkdirAll(filepath.Dir(issuePath), 0755); err != nil {
		t.Fatal(err)
	}

	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{Command: `cat > ` + issuePath + ` << 'EOF'
content
EOF`},
	}
	result := Check(testVaultDir, projectRoot, input)
	if result.Allowed {
		t.Fatal("expected shared nd-vault issue bash write to be blocked")
	}
	if result.Reason != projectIssuesBlockMsg {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckBash_BlocksSharedProjectIssuesRelativePath(t *testing.T) {
	projectRoot, sharedVault := setupPaivotWorktree(t)
	issuePath := filepath.Join(sharedVault, "issues", "PROJ-a1b2.md")
	if err := os.MkdirAll(filepath.Dir(issuePath), 0755); err != nil {
		t.Fatal(err)
	}
	relPath, err := filepath.Rel(projectRoot, issuePath)
	if err != nil {
		t.Fatal(err)
	}

	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{Command: `cat > ` + relPath + ` << 'EOF'
content
EOF`},
	}
	result := Check(testVaultDir, projectRoot, input)
	if result.Allowed {
		t.Fatal("expected relative shared nd-vault issue bash write to be blocked")
	}
	if result.Reason != projectIssuesBlockMsg {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

// --- Interpreter write detection tests ---

func TestCheckBash_BlocksPython3Write(t *testing.T) {
	cmd := `python3 -c 'open("` + testVaultDir + `/methodology/evil.md","w").write("pwned")'`
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: cmd},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for python3 -c write to methodology/, got allowed")
	}
}

func TestCheckBash_BlocksNodeWrite(t *testing.T) {
	cmd := `node -e 'require("fs").writeFileSync("` + testVaultDir + `/decisions/evil.md", "pwned")'`
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: cmd},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for node -e write to decisions/, got allowed")
	}
}

func TestCheckBash_BlocksRubyWrite(t *testing.T) {
	cmd := `ruby -e 'File.write("` + testVaultDir + `/patterns/evil.md", "pwned")'`
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: cmd},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for ruby -e write to patterns/, got allowed")
	}
}

func TestCheckBash_BlocksInterpreterWriteProjectVault(t *testing.T) {
	cmd := `python3 -c 'open("` + testProjectRoot + `/.vault/knowledge/patterns/evil.md","w").write("pwned")'`
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: cmd},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for python3 -c write to project vault, got allowed")
	}
}

func TestCheckFilePath_BlocksRelativeProjectVaultPath(t *testing.T) {
	// Relative path that resolves to project vault
	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/knowledge/../knowledge/patterns/test.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for relative path to project vault, got allowed")
	}
}

// --- Issue tracker (.vault/issues/) tests ---

func TestCheckFilePath_BlocksProjectIssues(t *testing.T) {
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/issues/PROJ-001.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for .vault/issues/ edit, got allowed")
	}
	if result.Reason != projectIssuesBlockMsg {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckFilePath_BlocksProjectIssuesWrite(t *testing.T) {
	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/issues/PROJ-002.md"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for .vault/issues/ write, got allowed")
	}
}

func TestCheckFilePath_AllowsOtherVaultPaths(t *testing.T) {
	// Files outside .vault/knowledge/ and .vault/issues/ should be allowed
	input := HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: testProjectRoot + "/.vault/.dispatcher-state.json"},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected allowed for .vault/.dispatcher-state.json, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsNdCommands(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `nd create --title="New issue" --description="Test"`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected nd command allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsNdUpdate(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `nd update PROJ-001 --status=in_progress`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected nd update allowed, got blocked: %s", result.Reason)
	}
}

func TestCheckBash_AllowsNdClose(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `nd close PROJ-001 PROJ-002`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected nd close allowed, got blocked: %s", result.Reason)
	}
}

func TestCheck_ProjectVaultStillProtectedWithoutSystemVault(t *testing.T) {
	dir := setupFSMProject(t, false, "", "")
	input := HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: dir + "/.vault/knowledge/decisions/test.md"},
	}
	result := Check("", dir, input)
	if result.Allowed {
		t.Errorf("expected project vault write blocked without system vault, got allowed")
	}
}

func TestCheck_FSMStillProtectedWithoutSystemVault(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `nd close PROJ-a1b2`},
	}
	result := Check("", dir, input)
	if result.Allowed {
		t.Errorf("expected nd close blocked without system vault, got allowed")
	}
}

func TestCheckBash_BlocksProjectIssuesRedirect(t *testing.T) {
	input := HookInput{
		ToolName: "Bash",
		ToolInput: ToolInput{Command: `cat > ` + testProjectRoot + `/.vault/issues/PROJ-001.md << 'EOF'
---
status: pending
---
content
EOF`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for cat > .vault/issues/, got allowed")
	}
	if result.Reason != projectIssuesBlockMsg {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestCheckBash_BlocksProjectIssuesCp(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cp /tmp/issue.md ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for cp to .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksProjectIssuesMv(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `mv /tmp/issue.md ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for mv to .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksRelativeProjectIssuesWrite(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cp /tmp/issue.md .vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for relative write to .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksProjectIssuesSedInPlace(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `sed -i 's/pending/in_progress/' ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for sed -i on .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksProjectIssuesRm(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `rm ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for rm on .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksNDPipeToProjectIssues(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `nd list --json | tee .vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for piped nd command into .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksProjectIssuesTee(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `echo "new content" | tee ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for tee to .vault/issues/, got allowed")
	}
}

func TestCheckBash_BlocksPythonWriteToIssues(t *testing.T) {
	cmd := `python3 -c 'open("` + testProjectRoot + `/.vault/issues/PROJ-001.md","w").write("pwned")'`
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: cmd},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if result.Allowed {
		t.Errorf("expected blocked for python3 -c write to .vault/issues/, got allowed")
	}
}

func TestCheckBash_AllowsReadFromIssues(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: `cat ` + testProjectRoot + `/.vault/issues/PROJ-001.md`},
	}
	result := Check(testVaultDir, testProjectRoot, input)
	if !result.Allowed {
		t.Errorf("expected read from .vault/issues/ allowed, got blocked: %s", result.Reason)
	}
}
