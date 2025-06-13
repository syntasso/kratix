package ptr

func To[A any](input A) *A {
	return &input
}

func True() *bool {
	return To(true)
}

func False() *bool {
	return To(false)
}
