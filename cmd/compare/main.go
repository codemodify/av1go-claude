// Command compare compares two Y4M files frame-by-frame and reports pixel diffs.
package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <file1.y4m> <file2.y4m>\n", os.Args[0])
		os.Exit(1)
	}

	f1, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer f1.Close()

	f2, err := os.Open(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer f2.Close()

	r1 := bufio.NewReader(f1)
	r2 := bufio.NewReader(f2)

	// Parse Y4M headers.
	hdr1, _ := r1.ReadString('\n')
	_, _ = r2.ReadString('\n')

	// Parse dimensions from Y4M header.
	w := 0
	h := 0
	fmt.Sscanf(hdr1, "YUV4MPEG2 W%d H%d", &w, &h)
	if w == 0 || h == 0 {
		fmt.Fprintf(os.Stderr, "error: could not parse Y4M header: %s\n", hdr1)
		os.Exit(1)
	}
	fmt.Printf("Dimensions: %dx%d\n", w, h)
	ySize := w * h
	chromaW := (w + 1) / 2
	chromaH := (h + 1) / 2
	uSize := chromaW * chromaH
	vSize := uSize

	y1 := make([]byte, ySize)
	y2 := make([]byte, ySize)
	u1 := make([]byte, uSize)
	u2 := make([]byte, uSize)
	v1 := make([]byte, vSize)
	v2 := make([]byte, vSize)


	for frame := 0; ; frame++ {
		// Read FRAME header for each frame
		fh1, err1 := r1.ReadString('\n')
		fh2, err2 := r2.ReadString('\n')
		if err1 != nil || err2 != nil {
			break
		}
		_ = fh1
		_ = fh2

		n1 := readFull(r1, y1)
		readFull(r1, u1)
		readFull(r1, v1)
		n2 := readFull(r2, y2)
		readFull(r2, u2)
		readFull(r2, v2)

		if n1 < ySize || n2 < ySize {
			break
		}

		yDiffs, maxYDiff := countDiffs(y1, y2)
		uDiffs, maxUDiff := countDiffs(u1, u2)
		vDiffs, maxVDiff := countDiffs(v1, v2)

		totalDiffs := yDiffs + uDiffs + vDiffs
		_ = totalDiffs

		if yDiffs == 0 && uDiffs == 0 && vDiffs == 0 {
			fmt.Printf("Frame %d: PERFECT\n", frame)
		} else {
			fmt.Printf("Frame %d: Y=%d/%d(max=%d) U=%d/%d(max=%d) V=%d/%d(max=%d)\n",
				frame, yDiffs, ySize, maxYDiff, uDiffs, uSize, maxUDiff, vDiffs, vSize, maxVDiff)
			// Find very first differing Y pixel
			for i := 0; i < ySize; i++ {
				if y1[i] != y2[i] {
					x := i % w
					y := i / w
					fmt.Printf("  FIRST Y diff at [%d,%d] (px %d): ours=%d ref=%d diff=%d\n", x, y, i, y1[i], y2[i], int(y1[i])-int(y2[i]))
					break
				}
			}
			// Print first few Y diffs
			count := 0
			for i := 0; i < ySize && count < 5; i++ {
				if y1[i] != y2[i] {
					x := i % w
					y := i / w
					fmt.Printf("  Y[%d,%d]: ours=%d ref=%d diff=%d\n", x, y, y1[i], y2[i], int(y1[i])-int(y2[i]))
					count++
				}
			}
			// Print first few U diffs
			count = 0
			for i := 0; i < uSize && count < 5; i++ {
				if u1[i] != u2[i] {
					x := i % chromaW
					y := i / chromaW
					fmt.Printf("  U[%d,%d]: ours=%d ref=%d diff=%d\n", x, y, u1[i], u2[i], int(u1[i])-int(u2[i]))
					count++
				}
			}
			// Print first few V diffs
			count = 0
			for i := 0; i < vSize && count < 5; i++ {
				if v1[i] != v2[i] {
					x := i % chromaW
					y := i / chromaW
					fmt.Printf("  V[%d,%d]: ours=%d ref=%d diff=%d\n", x, y, v1[i], v2[i], int(v1[i])-int(v2[i]))
					count++
				}
			}
		}
	}
}

func countDiffs(a, b []byte) (diffs, maxDiff int) {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d != 0 {
			diffs++
			if d < 0 {
				d = -d
			}
			if d > maxDiff {
				maxDiff = d
			}
		}
	}
	return
}

func readFull(r *bufio.Reader, buf []byte) int {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			break
		}
	}
	return n
}
