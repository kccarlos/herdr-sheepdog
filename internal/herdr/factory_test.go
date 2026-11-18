package herdr

import "testing"

func TestIsFactoryQuiet(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "nil", labels: nil, want: false},
		{name: "empty", labels: map[string]string{}, want: false},
		{name: "non-factory space labels", labels: map[string]string{"herdr.space": "s1", "herdr.tab": "t1"}, want: false},
		{name: "legacy herdr role without sheepdog label", labels: map[string]string{"herdr.role": "reviewer", "herdr.task": "FAC#1"}, want: false},
		{name: "factory coordinator", labels: map[string]string{"sheepdog.project": "proj", "sheepdog.role": "coordinator"}, want: false},
		{name: "factory worker", labels: map[string]string{"sheepdog.project": "proj", "sheepdog.role": "worker", "sheepdog.task": "proj#1"}, want: true},
		{name: "factory worker without task", labels: map[string]string{"sheepdog.project": "proj", "sheepdog.role": "worker"}, want: true},
		{name: "factory reviewer", labels: map[string]string{"sheepdog.project": "proj", "sheepdog.role": "reviewer", "sheepdog.task": "proj#1"}, want: true},
		{name: "worker-spawned reviewer without project", labels: map[string]string{"sheepdog.role": "reviewer", "sheepdog.task": "proj#1"}, want: true},
		{name: "factory checks pane", labels: map[string]string{"sheepdog.project": "proj", "sheepdog.role": "checks"}, want: true},
		{name: "factory helper pane", labels: map[string]string{"sheepdog.role": "helper", "sheepdog.task": "proj#1"}, want: true},
		{name: "task label without role", labels: map[string]string{"sheepdog.task": "proj#1"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFactoryQuiet(tc.labels); got != tc.want {
				t.Fatalf("IsFactoryQuiet(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}
