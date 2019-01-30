package metrics

import (
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	publisherLabelKeys = []string{
		labelKeyHandlerName,
		labelKeyPublisherName,
	}
)

type PublisherPrometheusMetricsDecorator struct {
	pub message.Publisher

	publisherSuccessTotal *prometheus.CounterVec
	publisherFailTotal    *prometheus.CounterVec
	publishTimeSeconds    *prometheus.HistogramVec
	publisherCountTotal   prometheus.Gauge
}

// Publish updates the relevant publisher metrics and calls the wrapped publisher's Publish.
func (m PublisherPrometheusMetricsDecorator) Publish(topic string, messages ...*message.Message) (err error) {
	if len(messages) == 0 {
		return m.pub.Publish(topic)
	}

	// TODO: take ctx not only from first msg. Might require changing the signature of Publish, which is planned anyway.
	ctx := messages[0].Context()
	labels := labelsFromCtx(ctx, publisherLabelKeys...)
	now := time.Now()

	defer func() {
		m.publishTimeSeconds.With(labels).Observe(time.Since(now).Seconds())
	}()
	defer func() {
		if err != nil {
			m.publisherFailTotal.With(labels).Inc()
			return
		}
		m.publisherSuccessTotal.With(labels).Inc()
	}()
	return m.pub.Publish(topic, messages...)
}

// Close decreases the total publisher count, closes the Prometheus HTTP server and calls wrapped Close.
func (m PublisherPrometheusMetricsDecorator) Close() error {
	m.publisherCountTotal.Dec()
	return m.pub.Close()
}

// DecoratePublisher wraps the underlying publisher with Prometheus metrics.
func (b PrometheusMetricsBuilder) DecoratePublisher(pub message.Publisher) (message.Publisher, error) {
	var err, registerErr error
	d := PublisherPrometheusMetricsDecorator{
		pub: pub,
	}

	d.publisherSuccessTotal, registerErr = b.registerCounterVec(prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: b.Namespace,
			Subsystem: b.Subsystem,
			Name:      "publisher_success_total",
			Help:      "Total number of successfully produced messages",
		},
		publisherLabelKeys,
	))
	if registerErr != nil {
		err = multierror.Append(err, registerErr)
	}

	d.publisherFailTotal, registerErr = b.registerCounterVec(prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: b.Namespace,
			Subsystem: b.Subsystem,
			Name:      "publisher_fail_total",
			Help:      "Total number of failed attempts to publish a message",
		},
		publisherLabelKeys,
	))
	if registerErr != nil {
		err = multierror.Append(err, registerErr)
	}

	d.publishTimeSeconds, registerErr = b.registerHistogramVec(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: b.Namespace,
			Subsystem: b.Subsystem,
			Name:      "publish_time_seconds",
			Help:      "The time that a publishing attempt (success or not) took in seconds",
		},
		publisherLabelKeys,
	))
	if registerErr != nil {
		err = multierror.Append(err, registerErr)
	}

	// todo: unclear if decrementing the gauge when publisher dies is trustworthy
	// don't register yet, WIP
	d.publisherCountTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: b.Namespace,
			Subsystem: b.Subsystem,
			Name:      "publisher_count_total",
			Help:      "The total count of active publishers",
		},
	)

	if err != nil {
		return nil, err
	}

	d.publisherCountTotal.Inc()
	return d, nil
}
