package common

type EnvironmentData struct {
	Temperature float32
	Humidity    float32
}

var emptyEnvironmentData EnvironmentData = EnvironmentData{}

func (ed EnvironmentData) IsEmpty() bool {
	return ed == emptyEnvironmentData
}
