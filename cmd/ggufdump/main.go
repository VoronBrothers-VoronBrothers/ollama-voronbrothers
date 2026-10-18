package main

import (
	"fmt"
	"os"

	"github.com/ollama/ollama/fs/gguf"
)

func main() {
	path := os.Args[1]
	f, err := gguf.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open error: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// Check for errors (validation happens lazily)
	if f.Err() != nil {
		fmt.Fprintf(os.Stderr, "VALIDATION ERROR: %v\n", f.Err())
		os.Exit(2)
	}

	count := 0
	for _, t := range f.TensorInfos() {
		count++
		nb := t.NumBytes()
		if nb <= 0 && count < 10 {
			fmt.Printf("WARN: tensor %q has NumBytes=%d\n", t.Name, nb)
		}
	}
	
	fmt.Printf("OK: parsed %d tensors without error\n", count)
	
	// Show output.weight specifically
	if ow := f.TensorInfo("output.weight"); ow.Name != "" {
		fmt.Printf("output.weight: type=%s(%d), NumBytes=%.1f MB\n", 
			ow.Type.String(), uint32(ow.Type), float64(ow.NumBytes())/1024/1024)
	}
}
