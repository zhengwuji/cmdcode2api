package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows launcher scripts have encoding requirements that are invisible in
// a diff but break the scripts outright when violated:
//
//   - *.ps1 is executed by powershell.exe (Windows PowerShell 5.1), which
//     decodes a BOM-less file using the system ANSI codepage. The scripts carry
//     non-ASCII messages, so a lost BOM turns them into mojibake and the parser
//     fails with "数组索引表达式缺失或无效" / "字符串缺少终止符" before a
//     single line runs. Editors and tooling strip BOMs routinely, so this is
//     easy to lose and hard to notice.
//   - *.bat is read by cmd.exe, which treats a leading BOM as literal text and
//     can defeat "@echo off"; labels and goto also want CRLF.
//
// These assertions run in CI so the requirement survives future edits.
func TestWindowsLauncherScriptEncoding(t *testing.T) {
	root := filepath.Join("..", "..")

	check := func(pattern string, wantBOM bool, wantCRLF bool, wantLF bool) {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("no files matched %s — launcher scripts moved?", pattern)
		}
		for _, path := range matches {
			name := filepath.Base(path)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			hasBOM := len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF
			nonASCII := false
			for _, b := range data {
				if b > 0x7F {
					nonASCII = true
					break
				}
			}

			if wantBOM && nonASCII && !hasBOM {
				t.Errorf("%s contains non-ASCII text but has no UTF-8 BOM; "+
					"powershell.exe will decode it with the ANSI codepage and fail to parse", name)
			}
			if !wantBOM && hasBOM {
				t.Errorf("%s must not start with a UTF-8 BOM (cmd.exe treats it as literal text)", name)
			}

			lf, crlf := 0, 0
			for i, b := range data {
				if b == '\n' {
					lf++
					if i > 0 && data[i-1] == '\r' {
						crlf++
					}
				}
			}
			if wantCRLF && lf > 0 && crlf != lf {
				t.Errorf("%s must use CRLF line endings (found %d LF, %d CRLF); "+
					"cmd.exe label and goto handling needs CRLF", name, lf, crlf)
			}
			if wantLF && crlf > 0 {
				t.Errorf("%s must use LF line endings (found %d CRLF); "+
					".gitattributes pins *.ps1 to eol=lf", name, crlf)
			}
		}
	}

	// .ps1: UTF-8 BOM required whenever the file has non-ASCII content, LF endings.
	check("*.ps1", true, false, true)
	// .bat: no BOM, CRLF endings.
	check("*.bat", false, true, false)

	// A .bat that invokes a .ps1 must keep doing so through powershell.exe -File,
	// because that is the invocation whose BOM sensitivity is documented above.
	bats, err := filepath.Glob(filepath.Join(root, "*.bat"))
	if err != nil {
		t.Fatalf("glob *.bat: %v", err)
	}
	for _, path := range bats {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		text := string(data)
		if strings.Contains(text, ".ps1") && !strings.Contains(text, "powershell.exe") {
			t.Errorf("%s references a .ps1 but does not launch it via powershell.exe",
				filepath.Base(path))
		}
	}
}
