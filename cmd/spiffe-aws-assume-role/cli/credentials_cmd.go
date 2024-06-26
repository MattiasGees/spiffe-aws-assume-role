package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/evalphobia/logrus_sentry"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/square/spiffe-aws-assume-role/pkg/credentials"
	"github.com/square/spiffe-aws-assume-role/pkg/processcreds"
	"github.com/square/spiffe-aws-assume-role/pkg/telemetry"
)

type CredentialsCmd struct {
	Audience                string        `required:"" help:"SVID JWT Audience. Must match AWS configuration"`
	SpiffeID                string        `optional:"" help:"The SPIFFE ID of this workload"`
	WorkloadSocket          string        `optional:"" help:"Path to SPIFFE Workload Socket"`
	RoleARN                 string        `required:"" help:"AWS Role ARN to assume"`
	SessionName             string        `optional:"" help:"AWS Session Name"`
	STSEndpoint             string        `optional:"" help:"AWS STS Endpoint"`
	STSRegion               string        `optional:"" help:"AWS STS Region"`
	SessionDuration         time.Duration `optional:"" type:"iso8601duration" help:"AWS session duration in ISO8601 duration format (e.g. PT5M for five minutes)"`
	LogFilePath             string        `optional:"" help:"Path to log file"`
	TelemetrySocket         string        `optional:"" help:"Socket address (TCP/UNIX) to emit metrics to (e.g. 127.0.0.1:8200)"`
	TelemetryName           string        `optional:"" help:"Service Name for Telemetry Data"`
	TelemetryServiceAsLabel bool          `optional:"" help:"Place the Service name as a label instead of prefix"`
	SentryDSN               string        `optional:"" help:"DSN from Sentry for sending errors (e.g.  https://<hash>@o123456.ingest.sentry.io/123456"`
	Debug                   bool          `optional:"" help:"Enable debug logging"`
}

func (c *CredentialsCmd) Run(context *CliContext) (err error) {
	c.configureLogger(context.Logger)
	c.configureSentry(context.Logger)

	t, err := c.configureTelemetry(context)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to configure telemetry for socket address %s", c.TelemetrySocket))
	}
	defer t.Close()
	context.Telemetry = t

	emitMetrics := t.Instrument(context.TelemetryOpts.OIDCMetricName, &err)
	defer emitMetrics()

	spiffeID := spiffeid.ID{}
	if c.SpiffeID != "" {
		spiffeID, err = spiffeid.FromString(c.SpiffeID)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("failed to parse SPIFFE ID from %s", c.SpiffeID))
		}
	}

	src := context.JWTSourceProvider(spiffeID, c.WorkloadSocket, c.Audience, context.Logger, t)

	session := createSession(c.STSEndpoint, c.STSRegion)
	stsClient := context.STSProvider(session)

	provider, err := credentials.NewProvider(
		c.Audience,
		c.RoleARN,
		src,
		c.SessionName,
		c.SessionDuration,
		stsClient,
		t,
		context.Logger)
	if err != nil {
		return errors.Wrap(err, "failed to instantiate credentials provider")
	}

	creds, err := processcreds.SerializeCredentials(provider)
	if err != nil {
		return errors.Wrap(err, "failed to serialize credentials")
	}

	_, err = fmt.Print(string(creds))
	return err
}

func (c *CredentialsCmd) configureLogger(logger *logrus.Logger) {
	if len(c.LogFilePath) > 0 {
		file, err := os.OpenFile(c.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			logger.Info(errors.Wrapf(err, "Failed to log to file %s, using default of stderr", c.LogFilePath))
		} else {
			logger.Out = io.MultiWriter(os.Stderr, file)
		}
	}
	if c.Debug {
		logger.Level = logrus.DebugLevel
	}
}

func (c *CredentialsCmd) configureTelemetry(context *CliContext) (t *telemetry.Telemetry, err error) {
	if c.TelemetrySocket != "" {
		context.TelemetryOpts.Socket = c.TelemetrySocket
	}

	if c.TelemetryName != "" {
		context.TelemetryOpts.ServiceName = c.TelemetryName
	}

	if c.TelemetryServiceAsLabel {
		context.TelemetryOpts.ServiceAsLabel = c.TelemetryServiceAsLabel
	}

	t, err = telemetry.NewTelemetry(context.TelemetryOpts)
	if err != nil {
		return nil, err
	}

	if len(c.STSRegion) > 0 {
		t.AddLabel("stsRegion", c.STSRegion)
	}

	for label, value := range context.TelemetryOpts.Labels {
		t.AddLabel(label, value)
	}

	return t, err
}

func (c *CredentialsCmd) configureSentry(logger *logrus.Logger) {
	if c.SentryDSN == "" {
		return
	}

	hook, err := logrus_sentry.NewSentryHook(c.SentryDSN, []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
	})

	if err != nil {
		logger.Fatalf("unable to initialize Sentry Hook %v", err)
	}

	logger.Hooks.Add(hook)
}

func createSession(stsEndpoint string, stsRegion string) *session.Session {
	config := &aws.Config{}

	if len(stsEndpoint) > 0 {
		config.Endpoint = aws.String(stsEndpoint)
	}

	if len(stsRegion) > 0 {
		config.Region = aws.String(stsRegion)
	}

	return session.Must(session.NewSession(config))
}
