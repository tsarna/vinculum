// Package aws implements the `client "aws"` block — a shared AWS
// configuration holder that provides credentials and region settings
// for child clients (sqs_sender, sqs_receiver, and future AWS service
// clients) to reference via `aws = client.<name>`.

package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("aws", process)
}

// AWSConnector is the interface child clients use to obtain the shared
// aws.Config from the base block.
type AWSConnector interface {
	cfg.Client
	Config() aws.Config
}

// AWSConfigDefinition is the HCL schema for `client "aws" "<name>"`.
type AWSConfigDefinition struct {
	Region         string         `hcl:"region"`
	AccessKeyID    hcl.Expression `hcl:"access_key_id,optional"`
	SecretAccessKey hcl.Expression `hcl:"secret_access_key,optional"`
	SessionToken   hcl.Expression `hcl:"session_token,optional"`
	RoleARN        string         `hcl:"role_arn,optional"`
	ExternalID     string         `hcl:"external_id,optional"`
	Endpoint       hcl.Expression `hcl:"endpoint,optional"`
	Profile        string         `hcl:"profile,optional"`
	DefRange       hcl.Range      `hcl:",def_range"`
}

// AWSClient is the runtime representation of a `client "aws"` block.
type AWSClient struct {
	cfg.BaseClient
	awsCfg aws.Config
}

func (c *AWSClient) Config() aws.Config { return c.awsCfg }

// CtyValue exposes the AWS client as a plain client capsule so child
// clients can extract it via type assertion to AWSConnector.
func (c *AWSClient) CtyValue() cty.Value {
	return cfg.NewClientCapsule(c)
}

func process(cfgObj *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := AWSConfigDefinition{}
	diags := gohcl.DecodeBody(remainingBody, cfgObj.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

	clientName := block.Labels[1]

	awsCfg, buildDiags := buildAWSConfig(cfgObj, &def)
	if buildDiags.HasErrors() {
		return nil, buildDiags
	}

	client := &AWSClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		awsCfg: awsCfg,
	}

	return client, nil
}

// buildAWSConfig constructs an aws.Config from the HCL definition.
func buildAWSConfig(cfgObj *cfg.Config, def *AWSConfigDefinition) (aws.Config, hcl.Diagnostics) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var opts []func(*config.LoadOptions) error

	// Region
	opts = append(opts, config.WithRegion(def.Region))

	// Profile
	if def.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(def.Profile))
	}

	// Static credentials
	accessKeyID, akDiags := evalOptionalString(cfgObj, def.AccessKeyID, "access_key_id")
	if akDiags.HasErrors() {
		return aws.Config{}, akDiags
	}
	secretAccessKey, skDiags := evalOptionalString(cfgObj, def.SecretAccessKey, "secret_access_key")
	if skDiags.HasErrors() {
		return aws.Config{}, skDiags
	}
	sessionToken, stDiags := evalOptionalString(cfgObj, def.SessionToken, "session_token")
	if stDiags.HasErrors() {
		return aws.Config{}, stDiags
	}

	if accessKeyID != "" && secretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, sessionToken),
		))
	}

	// Custom endpoint (for LocalStack, ElasticMQ, VPC endpoints)
	endpoint, epDiags := evalOptionalString(cfgObj, def.Endpoint, "endpoint")
	if epDiags.HasErrors() {
		return aws.Config{}, epDiags
	}
	if endpoint != "" {
		opts = append(opts, config.WithBaseEndpoint(endpoint))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "aws: failed to load AWS config",
			Detail:   err.Error(),
			Subject:  &def.DefRange,
		}}
	}

	// Assume role (must happen after base config is loaded so we have
	// initial credentials to call STS with)
	if def.RoleARN != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		assumeOpts := func(o *stscreds.AssumeRoleOptions) {
			if def.ExternalID != "" {
				o.ExternalID = &def.ExternalID
			}
		}
		awsCfg.Credentials = stscreds.NewAssumeRoleProvider(stsClient, def.RoleARN, assumeOpts)
	}

	return awsCfg, nil
}

// BuildDefaultConfig creates an aws.Config using only the default
// credential chain and a region. Used by SQS sender/receiver when no
// explicit `client "aws"` block is referenced.
func BuildDefaultConfig(ctx context.Context, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

func evalOptionalString(cfgObj *cfg.Config, expr hcl.Expression, name string) (string, hcl.Diagnostics) {
	if !cfg.IsExpressionProvided(expr) {
		return "", nil
	}
	val, diags := expr.Value(cfgObj.EvalCtx())
	if diags.HasErrors() {
		return "", diags
	}
	if val.IsNull() {
		return "", nil
	}
	if val.Type() != cty.String {
		r := expr.Range()
		return "", hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("aws: %s must be a string", name),
			Subject:  &r,
		}}
	}
	return val.AsString(), nil
}
