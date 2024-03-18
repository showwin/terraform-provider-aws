// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package datasync

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/service/datasync"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/errs/sdkdiag"
	"github.com/hashicorp/terraform-provider-aws/internal/flex"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
	"github.com/hashicorp/terraform-provider-aws/names"
)

// @SDKResource("aws_datasync_location_efs", name="Location EFS")
// @Tags(identifierAttribute="id")
func resourceLocationEFS() *schema.Resource {
	return &schema.Resource{
		CreateWithoutTimeout: resourceLocationEFSCreate,
		ReadWithoutTimeout:   resourceLocationEFSRead,
		UpdateWithoutTimeout: resourceLocationEFSUpdate,
		DeleteWithoutTimeout: resourceLocationEFSDelete,

		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"access_point_arn": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"arn": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"ec2_config": {
				Type:     schema.TypeList,
				Required: true,
				ForceNew: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"security_group_arns": {
							Type:     schema.TypeSet,
							Required: true,
							ForceNew: true,
							Elem: &schema.Schema{
								Type:         schema.TypeString,
								ValidateFunc: verify.ValidARN,
							},
						},
						"subnet_arn": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: verify.ValidARN,
						},
					},
				},
			},
			"efs_file_system_arn": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"file_system_access_role_arn": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"in_transit_encryption": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice(datasync.EfsInTransitEncryption_Values(), false),
			},
			"subdirectory": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  "/",
				// Ignore missing trailing slash
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					if new == "/" {
						return false
					}
					if strings.TrimSuffix(old, "/") == strings.TrimSuffix(new, "/") {
						return true
					}
					return false
				},
			},
			names.AttrTags:    tftags.TagsSchema(),
			names.AttrTagsAll: tftags.TagsSchemaComputed(),
			"uri": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},

		CustomizeDiff: verify.SetTagsDiff,
	}
}

func resourceLocationEFSCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).DataSyncConn(ctx)

	input := &datasync.CreateLocationEfsInput{
		Ec2Config:        expandEC2Config(d.Get("ec2_config").([]interface{})),
		EfsFilesystemArn: aws.String(d.Get("efs_file_system_arn").(string)),
		Subdirectory:     aws.String(d.Get("subdirectory").(string)),
		Tags:             getTagsIn(ctx),
	}

	if v, ok := d.GetOk("access_point_arn"); ok {
		input.AccessPointArn = aws.String(v.(string))
	}

	if v, ok := d.GetOk("file_system_access_role_arn"); ok {
		input.FileSystemAccessRoleArn = aws.String(v.(string))
	}

	if v, ok := d.GetOk("in_transit_encryption"); ok {
		input.InTransitEncryption = aws.String(v.(string))
	}

	output, err := conn.CreateLocationEfsWithContext(ctx, input)

	if err != nil {
		return sdkdiag.AppendErrorf(diags, "creating DataSync Location EFS: %s", err)
	}

	d.SetId(aws.StringValue(output.LocationArn))

	return append(diags, resourceLocationEFSRead(ctx, d, meta)...)
}

func resourceLocationEFSRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).DataSyncConn(ctx)

	output, err := findLocationEFSByARN(ctx, conn, d.Id())

	if !d.IsNewResource() && tfresource.NotFound(err) {
		log.Printf("[WARN] DataSync Location EFS (%s) not found, removing from state", d.Id())
		d.SetId("")
		return diags
	}

	if err != nil {
		return sdkdiag.AppendErrorf(diags, "reading DataSync Location EFS (%s): %s", d.Id(), err)
	}

	uri := aws.StringValue(output.LocationUri)
	globalId, err := globalIDFromLocationURI(uri)
	if err != nil {
		return sdkdiag.AppendFromErr(diags, err)
	}
	subdirectory, err := subdirectoryFromLocationURI(uri)
	if err != nil {
		return sdkdiag.AppendFromErr(diags, err)
	}

	locationArn, err := arn.Parse(d.Id())
	if err != nil {
		return sdkdiag.AppendFromErr(diags, err)
	}
	globalIdParts := strings.Split(globalId, ".") // Global ID format for EFS location is <region>.<efs_file_system_id>
	efsFileSystemArn := fmt.Sprintf("arn:%s:elasticfilesystem:%s:%s:file-system/%s", locationArn.Partition, globalIdParts[0], locationArn.AccountID, globalIdParts[1])
	d.Set("efs_file_system_arn", efsFileSystemArn)

	d.Set("access_point_arn", output.AccessPointArn)
	d.Set("arn", output.LocationArn)
	if err := d.Set("ec2_config", flattenEC2Config(output.Ec2Config)); err != nil {
		return sdkdiag.AppendErrorf(diags, "setting ec2_config: %s", err)
	}
	d.Set("file_system_access_role_arn", output.FileSystemAccessRoleArn)
	d.Set("in_transit_encryption", output.InTransitEncryption)
	d.Set("subdirectory", subdirectory)
	d.Set("uri", uri)

	return diags
}

func resourceLocationEFSUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	// Tags only.

	return append(diags, resourceLocationEFSRead(ctx, d, meta)...)
}

func resourceLocationEFSDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).DataSyncConn(ctx)

	log.Printf("[DEBUG] Deleting DataSync Location EFS: %s", d.Id())
	_, err := conn.DeleteLocationWithContext(ctx, &datasync.DeleteLocationInput{
		LocationArn: aws.String(d.Id()),
	})

	if tfawserr.ErrMessageContains(err, datasync.ErrCodeInvalidRequestException, "not found") {
		return diags
	}

	if err != nil {
		return sdkdiag.AppendErrorf(diags, "deleting DataSync Location EFS (%s): %s", d.Id(), err)
	}

	return diags
}

func findLocationEFSByARN(ctx context.Context, conn *datasync.DataSync, arn string) (*datasync.DescribeLocationEfsOutput, error) {
	input := &datasync.DescribeLocationEfsInput{
		LocationArn: aws.String(arn),
	}

	output, err := conn.DescribeLocationEfsWithContext(ctx, input)

	if tfawserr.ErrMessageContains(err, datasync.ErrCodeInvalidRequestException, "not found") {
		return nil, &retry.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}

	if err != nil {
		return nil, err
	}

	if output == nil {
		return nil, tfresource.NewEmptyResultError(input)
	}

	return output, nil
}

func flattenEC2Config(ec2Config *datasync.Ec2Config) []interface{} {
	if ec2Config == nil {
		return []interface{}{}
	}

	m := map[string]interface{}{
		"security_group_arns": flex.FlattenStringSet(ec2Config.SecurityGroupArns),
		"subnet_arn":          aws.StringValue(ec2Config.SubnetArn),
	}

	return []interface{}{m}
}

func expandEC2Config(l []interface{}) *datasync.Ec2Config {
	if len(l) == 0 || l[0] == nil {
		return nil
	}

	m := l[0].(map[string]interface{})

	ec2Config := &datasync.Ec2Config{
		SecurityGroupArns: flex.ExpandStringSet(m["security_group_arns"].(*schema.Set)),
		SubnetArn:         aws.String(m["subnet_arn"].(string)),
	}

	return ec2Config
}
