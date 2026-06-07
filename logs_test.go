package main

import (
	"bytes"
	"os"
	"testing"
)

func TestTailLines(t *testing.T) {
	cases := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{
			name:  "last 2 of 5 lines",
			input: "a\nb\nc\nd\ne\n",
			n:     2,
			want:  "d\ne\n",
		},
		{
			name:  "n larger than file",
			input: "a\nb\n",
			n:     10,
			want:  "a\nb\n",
		},
		{
			name:  "n=0 prints nothing",
			input: "a\nb\n",
			n:     0,
			want:  "",
		},
		{
			name:  "n=-1 prints all",
			input: "a\nb\nc\n",
			n:     -1,
			want:  "a\nb\nc\n",
		},
		{
			name:  "empty file",
			input: "",
			n:     5,
			want:  "",
		},
		{
			name:  "exactly n lines",
			input: "x\ny\nz\n",
			n:     3,
			want:  "x\ny\nz\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			f, err := os.CreateTemp(t.TempDir(), "tail-*.log")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(tc.input); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}

			// Act
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			tailErr := tailLines(f, tc.n)
			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			buf.ReadFrom(r)

			// Assert
			if tailErr != nil {
				t.Fatalf("tailLines error: %v", tailErr)
			}
			if got := buf.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
