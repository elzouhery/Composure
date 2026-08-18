package main

import (
	"fmt"
	"github.com/elzouhery/composure/internal/dockerfile"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	files, _ := filepath.Glob("testdata/dockerfiles/*")
	for _, p := range files {
		src, _ := os.ReadFile(p)
		f := dockerfile.Parse(src)
		fmt.Printf("\n######## %s  (escape=%q, stages=%d, instructions=%d)\n", p, f.EscapeChar, len(f.Stages()), len(f.Instructions))
		for _, si := range f.Stages() {
			in := f.Instructions[si]
			fmt.Printf("   FROM lines %d-%d  image=%q stage=%q platform=%q\n", in.StartLine, in.EndLine, in.ImageRef, in.StageName, in.PlatformFlag)
		}
		out, err := f.SetBaseImage(0, "example.invalid/base:v9.9.9")
		if err != nil {
			fmt.Println("   ERR:", err)
			continue
		}
		os.WriteFile("/tmp/a", src, 0644)
		os.WriteFile("/tmp/b", out, 0644)
		d, _ := exec.Command("diff", "-u", "--label", "before", "--label", "after", "/tmp/a", "/tmp/b").Output()
		fmt.Print(string(d))
	}
}
