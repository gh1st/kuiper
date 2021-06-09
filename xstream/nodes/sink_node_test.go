package nodes

import (
	"fmt"
	"github.com/emqx/kuiper/common"
	"github.com/emqx/kuiper/xstream/contexts"
	"github.com/emqx/kuiper/xstream/topotest/mocknodes"
	"reflect"
	"testing"
	"time"
)

func TestSinkTemplate_Apply(t *testing.T) {
	common.InitConf()
	var tests = []struct {
		config map[string]interface{}
		data   []byte
		result [][]byte
	}{
		{
			config: map[string]interface{}{
				"sendSingle":   true,
				"dataTemplate": `{"wrapper":"w1","content":{{json .}},"ab":"{{.ab}}"}`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`{"wrapper":"w1","content":{"ab":"hello1"},"ab":"hello1"}`), []byte(`{"wrapper":"w1","content":{"ab":"hello2"},"ab":"hello2"}`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `{"wrapper":"arr","content":{{json .}},"content0":{{json (index . 0)}},ab0":"{{index . 0 "ab"}}"}`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`{"wrapper":"arr","content":[{"ab":"hello1"},{"ab":"hello2"}],"content0":{"ab":"hello1"},ab0":"hello1"}`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `<div>results</div><ul>{{range .}}<li>{{.ab}}</li>{{end}}</ul>`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`<div>results</div><ul><li>hello1</li><li>hello2</li></ul>`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `{"content":{{json .}}}`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`{"content":[{"ab":"hello1"},{"ab":"hello2"}]}`)},
		}, {
			config: map[string]interface{}{
				"sendSingle":   true,
				"dataTemplate": `{"newab":"{{.ab}}"}`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`{"newab":"hello1"}`), []byte(`{"newab":"hello2"}`)},
		}, {
			config: map[string]interface{}{
				"sendSingle":   true,
				"dataTemplate": `{"newab":"{{.ab}}"}`,
			},
			data:   []byte(`[{"ab":"hello1"},{"ab":"hello2"}]`),
			result: [][]byte{[]byte(`{"newab":"hello1"}`), []byte(`{"newab":"hello2"}`)},
		}, {
			config: map[string]interface{}{
				"sendSingle":   true,
				"dataTemplate": `{"__meta":{{json .__meta}},"temp":{{.temperature}}}`,
			},
			data:   []byte(`[{"temperature":33,"humidity":70,"__meta": {"messageid":45,"other": "mock"}}]`),
			result: [][]byte{[]byte(`{"__meta":{"messageid":45,"other":"mock"},"temp":33}`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `[{"__meta":{{json (index . 0 "__meta")}},"temp":{{index . 0 "temperature"}}}]`,
			},
			data:   []byte(`[{"temperature":33,"humidity":70,"__meta": {"messageid":45,"other": "mock"}}]`),
			result: [][]byte{[]byte(`[{"__meta":{"messageid":45,"other":"mock"},"temp":33}]`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `[{{range $index, $ele := .}}{{if $index}},{{end}}{"result":{{add $ele.temperature $ele.humidity}}}{{end}}]`,
			},
			data:   []byte(`[{"temperature":33,"humidity":70},{"temperature":22,"humidity":50},{"temperature":11,"humidity":90}]`),
			result: [][]byte{[]byte(`[{"result":103},{"result":72},{"result":101}]`)},
		}, {
			config: map[string]interface{}{
				"dataTemplate": `{{$counter := 0}}{{range $index, $ele := .}}{{if ne 90.0 $ele.humidity}}{{$counter = add $counter 1}}{{end}}{{end}}{"result":{{$counter}}}`,
			},
			data:   []byte(`[{"temperature":33,"humidity":70},{"temperature":22,"humidity":50},{"temperature":11,"humidity":90}]`),
			result: [][]byte{[]byte(`{"result":2}`)},
		},
	}
	fmt.Printf("The test bucket size is %d.\n\n", len(tests))
	contextLogger := common.Log.WithField("rule", "TestSinkTemplate_Apply")
	ctx := contexts.WithValue(contexts.Background(), contexts.LoggerKey, contextLogger)

	for i, tt := range tests {
		mockSink := mocknodes.NewMockSink()
		s := NewSinkNodeWithSink("mockSink", mockSink, tt.config)
		s.Open(ctx, make(chan error))
		s.input <- tt.data
		time.Sleep(1 * time.Second)
		s.close(ctx, contextLogger)
		results := mockSink.GetResults()
		if !reflect.DeepEqual(tt.result, results) {
			t.Errorf("%d \tresult mismatch:\n\nexp=%s\n\ngot=%s\n\n", i, tt.result, results)
		}
	}
}
