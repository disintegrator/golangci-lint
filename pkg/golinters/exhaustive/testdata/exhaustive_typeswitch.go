//golangcitest:args -Eexhaustive
//golangcitest:config_path testdata/exhaustive_typeswitch.yml
package testdata

type Shape interface {
	isShape()
}

type Circle struct{}

func (Circle) isShape() {}

type Square struct{}

func (Square) isShape() {}

func processShape(s Shape) {
	switch s.(type) { // want `missing cases in type switch: \*Circle, Square, \*Square`
	case Circle:
	}
}
