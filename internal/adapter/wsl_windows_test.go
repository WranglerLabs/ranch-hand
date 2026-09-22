//go:build windows

package adapter

import "testing"

func TestDecodeWSLUTF16Output(t *testing.T) {
	input := []byte{'U', 0, 'b', 0, 'u', 0, 'n', 0, 't', 0, 'u', 0, '\r', 0, '\n', 0}
	if output := decodeWSLText(input); output != "Ubuntu\r\n" {
		t.Fatalf("unexpected decoded WSL output %q", output)
	}
}
