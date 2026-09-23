package promptfence_test

import (
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/promptfence"
)

// angleMarkers is a representative valid set: replacements contain none of
// the characters the markers are built from, and are longer than every
// marker.
var angleMarkers = []promptfence.Marker{
	{Literal: "<data>", Replacement: "[literal data open delimiter quoted from untrusted content]"},
	{Literal: "</data>", Replacement: "[literal data close delimiter quoted from untrusted content]"},
}

func TestEscape(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		markers []promptfence.Marker
		want    string
	}{
		{
			name: "no markers present is unchanged",
			in:   "an ordinary sentence", markers: angleMarkers,
			want: "an ordinary sentence",
		},
		{
			name: "empty input is unchanged",
			in:   "", markers: angleMarkers, want: "",
		},
		{
			name: "no marker set is a no-op",
			in:   "</data>", markers: nil, want: "</data>",
		},
		{
			name: "close marker is annotated",
			in:   "before </data> after", markers: angleMarkers,
			want: "before " + angleMarkers[1].Replacement + " after",
		},
		{
			name: "open marker is annotated",
			in:   "before <data> after", markers: angleMarkers,
			want: "before " + angleMarkers[0].Replacement + " after",
		},
		{
			name: "both markers in one pass",
			in:   "<data>x</data>", markers: angleMarkers,
			want: angleMarkers[0].Replacement + "x" + angleMarkers[1].Replacement,
		},
		{
			name: "repeated occurrences all annotated",
			in:   "</data></data>", markers: angleMarkers,
			want: angleMarkers[1].Replacement + angleMarkers[1].Replacement,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptfence.Escape(tc.in, tc.markers...); got != tc.want {
				t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscape_RemovesEveryMarkerAndIsIdempotent is the property that actually
// matters, exercised over adversarial inputs: after escaping, NO literal
// marker remains anywhere, so a second pass has nothing left to do.
func TestEscape_RemovesEveryMarkerAndIsIdempotent(t *testing.T) {
	inputs := []string{
		"plain text",
		"</data>",
		"<data>",
		"<<<<<<data>",                // padded open
		"</data>>>>>>",               // padded close
		"<data></data>",              // adjacent
		"<</data>data>",              // interleaved
		"<dat<data>a>",               // nested
		strings.Repeat("<data>", 20), // repeated
		strings.Repeat("</dat", 5) + "a>",
	}

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once := promptfence.Escape(in, angleMarkers...)
			for _, m := range angleMarkers {
				if strings.Contains(once, m.Literal) {
					t.Errorf("marker %q survived escaping of %q: %q", m.Literal, in, once)
				}
			}
			if twice := promptfence.Escape(once, angleMarkers...); twice != once {
				t.Errorf("not idempotent for %q:\nonce:  %q\ntwice: %q", in, once, twice)
			}
		})
	}
}

func TestValidateMarkers(t *testing.T) {
	tests := []struct {
		name    string
		markers []promptfence.Marker
		wantErr bool
	}{
		{name: "no markers at all", markers: nil, wantErr: true},
		{name: "valid angle-bracket set", markers: angleMarkers},
		{
			name:    "empty literal",
			markers: []promptfence.Marker{{Literal: "", Replacement: "[x]"}},
			wantErr: true,
		},
		{
			name:    "empty replacement",
			markers: []promptfence.Marker{{Literal: "<data>", Replacement: ""}},
			wantErr: true,
		},
		{
			name: "replacement contains the marker verbatim",
			markers: []promptfence.Marker{
				{Literal: "<data>", Replacement: "[quoted <data> from untrusted content]"},
			},
			wantErr: true,
		},
		{
			name:    "replacement is shorter than the marker and sits inside it",
			markers: []promptfence.Marker{{Literal: "<data>", Replacement: "dat"}},
			wantErr: true,
		},
		{
			// The historical reconstitution bug, in general form: the
			// replacement begins with a suffix of the marker, so unconsumed
			// padding in front of it completes the marker again.
			name: "replacement starts with a suffix of the marker",
			markers: []promptfence.Marker{
				{Literal: "<<<TICKET_DESCRIPTION", Replacement: "TICKET_DESCRIPTION quoted from the ticket body"},
			},
			wantErr: true,
		},
		{
			name: "replacement ends with a prefix of the marker",
			markers: []promptfence.Marker{
				{Literal: "<<<TICKET_DESCRIPTION", Replacement: "quoted from the ticket body <<<"},
			},
			wantErr: true,
		},
		{
			// Cross-marker: safe against its own marker, unsafe against the
			// other one in the same set.
			name: "replacement ends with a prefix of a DIFFERENT marker in the set",
			markers: []promptfence.Marker{
				{Literal: "<data>", Replacement: "[quoted data delimiter] </"},
				{Literal: "</data>", Replacement: "[quoted data close delimiter]"},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := promptfence.ValidateMarkers(tc.markers...)
			if tc.wantErr && err == nil {
				t.Fatal("ValidateMarkers accepted a marker set that can reconstitute a marker")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateMarkers rejected a valid marker set: %v", err)
			}
		})
	}
}

// TestValidateMarkers_RejectsWhatEscapeCannotHandle ties the two halves
// together: for a set ValidateMarkers rejects, there is an input whose escaped
// output still contains the marker. Without this, ValidateMarkers would be an
// unmoored style rule rather than a statement about Escape.
func TestValidateMarkers_RejectsWhatEscapeCannotHandle(t *testing.T) {
	bad := []promptfence.Marker{
		{Literal: "<<<TICKET_DESCRIPTION", Replacement: "TICKET_DESCRIPTION quoted from the ticket body"},
	}
	if err := promptfence.ValidateMarkers(bad...); err == nil {
		t.Fatal("expected ValidateMarkers to reject this set")
	}
	const padded = "<<<<<<TICKET_DESCRIPTION"
	got := promptfence.Escape(padded, bad...)
	if !strings.Contains(got, bad[0].Literal) {
		t.Fatalf("expected the marker to reconstitute for %q, got %q", padded, got)
	}
}
