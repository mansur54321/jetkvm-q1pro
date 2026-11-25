#include <linux/miscdevice.h>
#include <linux/module.h>
#include <media/v4l2-device.h>
#include <media/v4l2-dev.h>

/* Test kernel devnames whose path differs from their sysfs class entry. */
static const struct file_operations misc_ops = { .owner = THIS_MODULE };
static struct miscdevice mpi = {
	.minor = MISC_DYNAMIC_MINOR, .name = "q1-test-mpi",
	.nodename = "mpi/q1-test", .fops = &misc_ops,
};
static struct miscdevice bsg = {
	.minor = MISC_DYNAMIC_MINOR, .name = "q1-test-bsg",
	.nodename = "bsg/q1-test", .fops = &misc_ops,
};
static struct v4l2_device v4l = { .name = "q1-mdev-test" };
static const struct v4l2_file_operations video_ops = { .owner = THIS_MODULE };
static struct video_device video = {
	.name = "q1-mdev-test", .v4l2_dev = &v4l, .fops = &video_ops,
	.release = video_device_release_empty, .device_caps = V4L2_CAP_VIDEO_CAPTURE,
};

static int __init q1_test_init(void)
{
	int ret = misc_register(&mpi);
	if (ret)
		return ret;
	ret = misc_register(&bsg);
	if (ret)
		goto undo_mpi;
	ret = v4l2_device_register(NULL, &v4l);
	if (ret)
		goto undo_bsg;
	ret = video_register_device(&video, VFL_TYPE_VIDEO, -1);
	if (!ret)
		return 0;
	v4l2_device_unregister(&v4l);
undo_bsg:
	misc_deregister(&bsg);
undo_mpi:
	misc_deregister(&mpi);
	return ret;
}

static void __exit q1_test_exit(void)
{
	video_unregister_device(&video);
	v4l2_device_unregister(&v4l);
	misc_deregister(&bsg);
	misc_deregister(&mpi);
}

module_init(q1_test_init);
module_exit(q1_test_exit);
MODULE_LICENSE("GPL");
