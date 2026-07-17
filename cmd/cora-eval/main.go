package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/claracore/cora/internal/cora"
)

func main() {
	input := flag.String("input", "", "Cora evaluation CSV path")
	productLine := flag.String("product-line", "", "experience-pack product line")
	experiencePackDir := flag.String("experience-pack-dir", "", "directory containing the private product-line experience pack")
	jsonPath := flag.String("json", "", "JSON output path")
	markdownPath := flag.String("markdown", "", "Markdown output path")
	flag.Parse()
	if *input == "" || *productLine == "" || *experiencePackDir == "" || (*jsonPath == "" && *markdownPath == "") {
		fmt.Fprintln(os.Stderr, "input, product-line, experience-pack-dir, and at least one of json or markdown output are required")
		os.Exit(2)
	}
	core, err := cora.LoadExperiencePacks(*experiencePackDir)
	if err != nil {
		fatal(err)
	}
	file, err := os.Open(*input)
	if err != nil {
		fatal(err)
	}
	defer file.Close()
	report, err := cora.EvaluateCoraCSV(context.Background(), file, *productLine, core)
	if err != nil {
		fatal(err)
	}
	if *jsonPath != "" {
		if err := writeFile(*jsonPath, func(file *os.File) error {
			encoder := json.NewEncoder(file)
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		}); err != nil {
			fatal(err)
		}
	}
	if *markdownPath != "" {
		if err := writeFile(*markdownPath, func(file *os.File) error {
			return cora.WriteShadowEvalMarkdown(file, report)
		}); err != nil {
			fatal(err)
		}
	}
}

func writeFile(path string, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cora-eval-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := write(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
