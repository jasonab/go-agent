package newrelic

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.datanerd.us/p/will/newrelic/internal"
)

var (
	sampleLicense = "0123456789012345678901234567890123456789"
	validParams   = map[string]interface{}{"zip": 1, "zap": 2}
)

var (
	helloResponse   = []byte("hello")
	helloPath       = "/hello"
	helloRequest, _ = http.NewRequest("GET", helloPath, nil)
)

func handler(w http.ResponseWriter, req *http.Request) {
	w.Write(helloResponse)
}

func BenchmarkMuxWithoutNewRelic(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc(helloPath, handler)

	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mux.ServeHTTP(w, helloRequest)
	}
}

func BenchmarkMuxWithNewRelic(b *testing.B) {
	app := testApp(nil, nil, b)
	mux := http.NewServeMux()
	mux.HandleFunc(WrapHandleFunc(app, helloPath, handler))

	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mux.ServeHTTP(w, helloRequest)
	}
}

func BenchmarkMuxDevelopmentMode(b *testing.B) {
	cfg := NewConfig("my app", sampleLicense)
	cfg.Development = true
	app, err := NewApplication(cfg)
	if nil != err {
		b.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(WrapHandleFunc(app, helloPath, handler))

	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mux.ServeHTTP(w, helloRequest)
	}
}

type TestApp struct {
	Application
	h *internal.Harvest
}

func (app *TestApp) Consume(id internal.AgentRunID, data internal.Harvestable) {
	data.MergeIntoHarvest(app.h)
}

func testApp(replyfn func(*internal.ConnectReply), cfgfn func(*Config), t testing.TB) *TestApp {
	cfg := NewConfig("my app", sampleLicense)
	cfg.Development = true

	if nil != cfgfn {
		cfgfn(&cfg)
	}

	app, err := newApp(cfg)
	if nil != err {
		t.Fatal(err)
	}

	if nil != replyfn {
		reply := internal.ConnectReplyDefaults()
		replyfn(reply)
		app.setRun(&appRun{ConnectReply: reply})
	}

	ta := &TestApp{Application: app, h: internal.NewHarvest(time.Now())}
	app.testConsumer = ta
	return ta
}

func TestRecordCustomEventSuccess(t *testing.T) {
	app := testApp(nil, nil, t)
	err := app.RecordCustomEvent("myType", validParams)
	if nil != err {
		t.Error(err)
	}
	app.h.ExpectCustomEvents(t, []internal.WantCustomEvent{{"myType", validParams}})
}

func TestRecordCustomEventHighSecurityEnabled(t *testing.T) {
	cfgfn := func(cfg *Config) { cfg.HighSecurity = true }
	app := testApp(nil, cfgfn, t)
	err := app.RecordCustomEvent("myType", validParams)
	if err != highSecurityEnabledError {
		t.Error(err)
	}
	app.h.ExpectCustomEvents(t, []internal.WantCustomEvent{})
}

func TestRecordCustomEventEventsDisabled(t *testing.T) {
	cfgfn := func(cfg *Config) { cfg.CustomEvents.Enabled = false }
	app := testApp(nil, cfgfn, t)
	err := app.RecordCustomEvent("myType", validParams)
	if err != customEventsDisabledError {
		t.Error(err)
	}
	app.h.ExpectCustomEvents(t, []internal.WantCustomEvent{})
}

func TestRecordCustomEventBadInput(t *testing.T) {
	app := testApp(nil, nil, t)
	err := app.RecordCustomEvent("????", validParams)
	if err != internal.EventTypeRegexError {
		t.Error(err)
	}
	app.h.ExpectCustomEvents(t, []internal.WantCustomEvent{})
}

func TestRecordCustomEventRemoteDisable(t *testing.T) {
	replyfn := func(reply *internal.ConnectReply) { reply.CollectCustomEvents = false }
	app := testApp(replyfn, nil, t)
	err := app.RecordCustomEvent("myType", validParams)
	if err != customEventsRemoteDisabledError {
		t.Error(err)
	}
	app.h.ExpectCustomEvents(t, []internal.WantCustomEvent{})
}

type sampleResponseWriter struct {
	code    int
	written int
	header  http.Header
}

func (w *sampleResponseWriter) Header() http.Header       { return w.header }
func (w *sampleResponseWriter) Write([]byte) (int, error) { return w.written, nil }
func (w *sampleResponseWriter) WriteHeader(x int)         { w.code = x }

func TestTxnResponseWriter(t *testing.T) {
	// NOTE: Eventually when the ResponseWriter is instrumented, this test
	// should be expanded to make sure that calling ResponseWriter methods
	// after the transaction has ended is not problematic.
	w := &sampleResponseWriter{
		header: make(map[string][]string),
	}
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", w, nil)
	w.header.Add("zip", "zap")
	if out := txn.Header(); out.Get("zip") != "zap" {
		t.Error(out.Get("zip"))
	}
	w.written = 123
	if out, _ := txn.Write(nil); out != 123 {
		t.Error(out)
	}
	if txn.WriteHeader(503); w.code != 503 {
		t.Error(w.code)
	}
}

type myError struct{}

func (e myError) Error() string { return "my msg" }

func TestNoticeErrorBackground(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if nil != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.TestNoticeErrorBackground",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorWeb(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, sampleRequest)
	err := txn.NoticeError(myError{})
	if nil != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "WebTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.TestNoticeErrorWeb",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "WebTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"WebTransaction/Pattern/myName", "", true, nil},
		{"WebTransaction", "", true, nil},
		{"HttpDispatcher", "", true, nil},
		{"Apdex", "", true, nil},
		{"Apdex/Pattern/myName", "", false, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allWeb", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/WebTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorTxnEnded(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	txn.End()
	err := txn.NoticeError(myError{})
	if err != internal.AlreadyEndedErr {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
	})
}

func TestNoticeErrorHighSecurity(t *testing.T) {
	cfgFn := func(cfg *Config) { cfg.HighSecurity = true }
	app := testApp(nil, cfgFn, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if nil != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     internal.HighSecurityErrorMsg,
		Klass:   "newrelic.myError",
		Caller:  "newrelic.TestNoticeErrorHighSecurity",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     internal.HighSecurityErrorMsg,
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorLocallyDisabled(t *testing.T) {
	cfgFn := func(cfg *Config) { cfg.ErrorCollector.Enabled = false }
	app := testApp(nil, cfgFn, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if internal.ErrorsLocallyDisabled != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorRemotelyDisabled(t *testing.T) {
	replyfn := func(reply *internal.ConnectReply) { reply.CollectErrors = false }
	app := testApp(replyfn, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if internal.ErrorsRemotelyDisabled != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorNil(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(nil)
	if internal.NilError != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
	})
}

func TestNoticeErrorEventsLocallyDisabled(t *testing.T) {
	cfgFn := func(cfg *Config) { cfg.ErrorCollector.CaptureEvents = false }
	app := testApp(nil, cfgFn, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if nil != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.TestNoticeErrorEventsLocallyDisabled",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestNoticeErrorEventsRemotelyDisabled(t *testing.T) {
	replyfn := func(reply *internal.ConnectReply) { reply.CollectErrorEvents = false }
	app := testApp(replyfn, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.NoticeError(myError{})
	if nil != err {
		t.Error(err)
	}
	txn.End()
	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.TestNoticeErrorEventsRemotelyDisabled",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

var (
	sampleRequest, _ = http.NewRequest("get", "newrelic.com", nil)
)

func TestTransactionEventWeb(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, sampleRequest)
	err := txn.End()
	if nil != err {
		t.Error(err)
	}
	app.h.ExpectTxnEvents(t, []internal.WantTxnEvent{
		{Name: "WebTransaction/Pattern/myName", Zone: "S"},
	})
}

func TestTransactionEventBackground(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)
	err := txn.End()
	if nil != err {
		t.Error(err)
	}
	app.h.ExpectTxnEvents(t, []internal.WantTxnEvent{
		{Name: "OtherTransaction/Pattern/myName"},
	})
}

func TestTransactionEventLocallyDisabled(t *testing.T) {
	cfgFn := func(cfg *Config) { cfg.TransactionEvents.Enabled = false }
	app := testApp(nil, cfgFn, t)
	txn := app.StartTransaction("myName", nil, sampleRequest)
	err := txn.End()
	if nil != err {
		t.Error(err)
	}
	app.h.ExpectTxnEvents(t, []internal.WantTxnEvent{})
}

func TestTransactionEventRemotelyDisabled(t *testing.T) {
	replyfn := func(reply *internal.ConnectReply) { reply.CollectAnalyticsEvents = false }
	app := testApp(replyfn, nil, t)
	txn := app.StartTransaction("myName", nil, sampleRequest)
	err := txn.End()
	if nil != err {
		t.Error(err)
	}
	app.h.ExpectTxnEvents(t, []internal.WantTxnEvent{})
}

func myErrorHandler(w http.ResponseWriter, req *http.Request) {
	w.Write([]byte("my response"))
	if txn, ok := w.(Transaction); ok {
		txn.NoticeError(myError{})
	}
}

func TestWrapHandleFunc(t *testing.T) {
	app := testApp(nil, nil, t)
	mux := http.NewServeMux()
	mux.HandleFunc(WrapHandleFunc(app, helloPath, myErrorHandler))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, helloRequest)

	out := w.Body.String()
	if "my response" != out {
		t.Error(out)
	}

	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "WebTransaction/Pattern/hello",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.myErrorHandler",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "WebTransaction/Pattern/hello",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"WebTransaction/Pattern/hello", "", true, nil},
		{"WebTransaction", "", true, nil},
		{"HttpDispatcher", "", true, nil},
		{"Apdex", "", true, nil},
		{"Apdex/Pattern/hello", "", false, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allWeb", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/WebTransaction/Pattern/hello", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestWrapHandle(t *testing.T) {
	app := testApp(nil, nil, t)
	mux := http.NewServeMux()
	mux.Handle(WrapHandle(app, helloPath, http.HandlerFunc(myErrorHandler)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, helloRequest)

	out := w.Body.String()
	if "my response" != out {
		t.Error(out)
	}

	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "WebTransaction/Pattern/hello",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "newrelic.myErrorHandler",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "WebTransaction/Pattern/hello",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"WebTransaction/Pattern/hello", "", true, nil},
		{"WebTransaction", "", true, nil},
		{"HttpDispatcher", "", true, nil},
		{"Apdex", "", true, nil},
		{"Apdex/Pattern/hello", "", false, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allWeb", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/WebTransaction/Pattern/hello", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestSetName(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("one", nil, nil)
	if err := txn.SetName("two"); nil != err {
		t.Error(err)
	}
	txn.End()
	if err := txn.SetName("three"); err != internal.AlreadyEndedErr {
		t.Error(err)
	}

	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/two", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
	})
}

func deferEndPanic(txn Transaction, panicMe interface{}) (r interface{}) {
	defer func() {
		r = recover()
	}()

	defer txn.End()

	panic(panicMe)
}

func TestPanicError(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)

	e := myError{}
	r := deferEndPanic(txn, e)
	if r != e {
		t.Error("panic not propogated", r)
	}

	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
		Caller:  "internal.(*txn).End",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my msg",
		Klass:   "newrelic.myError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestPanicString(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)

	e := "my string"
	r := deferEndPanic(txn, e)
	if r != e {
		t.Error("panic not propogated", r)
	}

	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my string",
		Klass:   "internal.panicError",
		Caller:  "internal.(*txn).End",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "my string",
		Klass:   "internal.panicError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestPanicInt(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)

	e := 22
	r := deferEndPanic(txn, e)
	if r != e {
		t.Error("panic not propogated", r)
	}

	app.h.ExpectErrors(t, []internal.WantError{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "22",
		Klass:   "internal.panicError",
		Caller:  "internal.(*txn).End",
	}})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{{
		TxnName: "OtherTransaction/Pattern/myName",
		Msg:     "22",
		Klass:   "internal.panicError",
	}})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
		{"Errors/all", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/allOther", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
		{"Errors/OtherTransaction/Pattern/myName", "", true, []float64{1, 0, 0, 0, 0, 0, 0}},
	})
}

func TestPanicNil(t *testing.T) {
	app := testApp(nil, nil, t)
	txn := app.StartTransaction("myName", nil, nil)

	r := deferEndPanic(txn, nil)
	if nil != r {
		t.Error(r)
	}

	app.h.ExpectErrors(t, []internal.WantError{})
	app.h.ExpectErrorEvents(t, []internal.WantErrorEvent{})
	app.h.ExpectMetrics(t, []internal.WantMetric{
		{"OtherTransaction/Pattern/myName", "", true, nil},
		{"OtherTransaction/all", "", true, nil},
	})
}