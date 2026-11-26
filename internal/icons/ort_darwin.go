package icons

import (
	"errors"
	"fmt"
	"slices"

	ort "github.com/getcharzp/onnxruntime_purego"
)

type ortEngine struct {
	session *ort.Session
	input   ort.TensorInfo
	output  string
}

func openORT(runtimePath, modelPath string, m *manifest) (engine, error) {
	rt, err := ort.NewEngine(runtimePath)
	if err != nil {
		return nil, fmt.Errorf("load the runtime: %w", err)
	}
	opts, err := rt.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Close()
	if err := opts.SetIntraOpNumThreads(m.Threads); err != nil {
		return nil, err
	}
	s, err := rt.NewSession(modelPath, opts)
	if err != nil {
		return nil, fmt.Errorf("load the model: %w", err)
	}
	if len(s.Inputs) != 1 || len(s.Outputs) != 1 {
		return nil, errors.New("the model does not have one input and one output")
	}
	if err := m.fits(s.Inputs[0].Shape, s.Outputs[0].Shape); err != nil {
		return nil, err
	}
	return &ortEngine{session: s, input: s.Inputs[0], output: s.Outputs[0].Name}, nil
}

func (e *ortEngine) run(tensor []float32) ([]float32, []int64, error) {
	in, err := ort.NewTensor(e.input.Shape, tensor)
	if err != nil {
		return nil, nil, err
	}
	defer in.Close()
	outs, err := e.session.Run(map[string]*ort.Value{e.input.Name: in})
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		for _, o := range outs {
			o.Close()
		}
	}()
	o := outs[e.output]
	shape, err := o.GetShape()
	if err != nil {
		return nil, nil, err
	}
	data, err := ort.GetTensorData[float32](o)
	if err != nil {
		return nil, nil, err
	}
	return slices.Clone(data), slices.Clone(shape), nil
}
