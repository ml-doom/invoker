package internal

type StateRestartArgs struct {
	ProjectName string   `validate:"required,varname"`
	Hosts       []string `validate:"required"`
	InvokerExec *string
}

var defaultInvokerExec = PtrTo("invoker")

type StateFetchArgs struct {
	ProjectName string `validate:"required,varname"`
}
