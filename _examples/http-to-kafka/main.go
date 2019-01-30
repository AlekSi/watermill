package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"math"
	"math/rand"
	stdHttp "net/http"
	_ "net/http/pprof"
	"time"

	"github.com/go-chi/chi"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	uuid "github.com/satori/go.uuid"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/infrastructure/http"
	"github.com/ThreeDotsLabs/watermill/message/infrastructure/kafka"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/ThreeDotsLabs/watermill/message/router/plugin"
	"github.com/ThreeDotsLabs/watermill/metrics"
)

var (
	kafkaAddr    = flag.String("kafka", "localhost:9092", "The address of the kafka broker")
	httpAddr     = flag.String("http", ":8080", "The address for the http subscriber")
	metricsAddr  = flag.String("metrics", ":8081", "The address that will expose /metrics for Prometheus")
	handlerDelay = flag.Float64("delay", 0, "The stdev of normal distribution of delay in handler, to simulate load")
)

type GitlabWebhook struct {
	ObjectKind string `json:"object_kind"`
}

func main() {
	flag.Parse()
	logger := watermill.NewStdLogger(true, true)

	kafkaPublisher, err := kafka.NewPublisher([]string{*kafkaAddr}, kafka.DefaultMarshaler{}, nil, logger)
	if err != nil {
		panic(err)
	}

	httpSubscriber, err := http.NewSubscriber(*httpAddr, func(topic string, request *stdHttp.Request) (*message.Message, error) {
		b, err := ioutil.ReadAll(request.Body)
		if err != nil {
			return nil, errors.Wrap(err, "cannot read body")
		}

		return message.NewMessage(uuid.NewV4().String(), b), nil
	}, logger)
	if err != nil {
		panic(err)
	}

	r, err := message.NewRouter(
		message.RouterConfig{},
		logger,
	)
	if err != nil {
		panic(err)
	}

	// todo: how to enforce that metrics are the last middleware?
	prometheusRegistry := prometheus.NewRegistry()
	metrics.AddPrometheusRouterMetrics(r, prometheusRegistry, "", "")

	r.AddMiddleware(
		middleware.Recoverer,
		middleware.CorrelationID,
	)
	r.AddPlugin(plugin.SignalsHandler)

	err = r.AddHandler(
		"http_to_kafka",
		"/gitlab-webhooks", // this is the URL of our API
		httpSubscriber,
		"webhooks",
		kafkaPublisher,
		func(msg *message.Message) ([]*message.Message, error) {
			delay(*handlerDelay)
			webhook := GitlabWebhook{}

			// simple validation
			if err := json.Unmarshal(msg.Payload, &webhook); err != nil {
				return nil, errors.Wrap(err, "cannot unmarshal message")
			}
			if webhook.ObjectKind == "" {
				return nil, errors.New("empty object kind")
			}

			// just forward from http subscriber to kafka publisher
			return []*message.Message{msg}, nil
		},
	)
	if err != nil {
		panic(err)
	}

	go func() {
		// HTTP server needs to be started after router is ready.
		<-r.Running()
		_ = httpSubscriber.StartHTTPServer()
	}()

	wait := make(chan struct{})
	go metricsServer(prometheusRegistry, wait)

	_ = r.Run()
	close(wait)
}

func metricsServer(prometheusRegistry *prometheus.Registry, wait chan struct{}) {
	router := chi.NewRouter()
	handler := promhttp.HandlerFor(prometheusRegistry, promhttp.HandlerOpts{})
	router.Get("/metrics", func(w stdHttp.ResponseWriter, r *stdHttp.Request) {
		handler.ServeHTTP(w, r)
	})
	server := stdHttp.Server{
		Addr:    *metricsAddr,
		Handler: handler,
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil {
			panic(err)
		}
	}()

	<-wait
	server.Close()
}

func delay(seconds float64) {
	if seconds == 0 {
		return
	}
	delay := math.Abs(rand.NormFloat64() * seconds)
	time.Sleep(time.Duration(float64(time.Second) * delay))
}
