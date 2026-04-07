package main

import (
	"av1go/decoder"
	"fmt"
)

func main() {
	// TX_16X64, DCT_DCT, coefficients at (row=0, col=9..12) = 32, -32, 32, -32
	w, h := 16, 64
	coeffs := make([]int32, w*h)
	coeffs[0*w+9] = 32
	coeffs[0*w+10] = -32
	coeffs[0*w+11] = 32
	coeffs[0*w+12] = -32

	decoder.InverseTransform2D(coeffs, w, h, decoder.DCT_DCT)

	fmt.Printf("OUR_ITX_RES w=%d h=%d\n", w, h)
	for i := 0; i < w*h; i++ {
		if coeffs[i] != 0 {
			fmt.Printf("  r[%d]=%d\n", i, coeffs[i])
		}
	}
}
