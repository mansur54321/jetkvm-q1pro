/* Test-only ALSA timing faults. All PCM data, configuration, recovery and state are real libasound. */
#define _GNU_SOURCE
#include <alsa/asoundlib.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>
#include <unistd.h>

static bool injected, recovered;
static struct timespec paused_at;

static void flag(const char *name)
{
	char path[512];
	snprintf(path, sizeof(path), "%s/%s", getenv("Q1_WEB_TEST"), name);
	int fd = open(path, O_WRONLY | O_CREAT | O_CLOEXEC, 0600);
	if (fd < 0) abort();
	close(fd);
}

static double elapsed(void)
{
	struct timespec now;
	clock_gettime(CLOCK_MONOTONIC, &now);
	if (!paused_at.tv_sec) {
		char path[512];
		snprintf(path, sizeof(path), "%s/pause-clock", getenv("Q1_WEB_TEST"));
		if (access(path, F_OK) != 0) return -1;
		paused_at = now;
	}
	return (double)(now.tv_sec - paused_at.tv_sec) +
		(double)(now.tv_nsec - paused_at.tv_nsec) / 1e9;
}

int snd_pcm_wait(snd_pcm_t *pcm, int timeout)
{
	double gap = elapsed();
	if (gap >= 0 && gap < 6) {
		usleep(1000 * (unsigned)(timeout > 0 && timeout <= 100 ? timeout : 100));
		return 0;
	}
	return ((__typeof__(&snd_pcm_wait))dlsym(RTLD_NEXT,"snd_pcm_wait"))(pcm,timeout);
}

int snd_pcm_open(snd_pcm_t **pcm, const char *name, snd_pcm_stream_t stream, int mode)
{
	int rc = ((__typeof__(&snd_pcm_open))dlsym(RTLD_NEXT,"snd_pcm_open"))(pcm,name,stream,mode);
	fprintf(stderr,"Q1 test PCM open rc=%d\n",rc);
	double gap = elapsed();
	if (rc == 0 && stream == SND_PCM_STREAM_CAPTURE && gap >= 3.5 && gap < 6)
		flag("reopened-while-clock-idle");
	return rc;
}

snd_pcm_sframes_t snd_pcm_readi(snd_pcm_t *pcm, void *buf, snd_pcm_uframes_t frames)
{
	if (!injected && elapsed() >= 6) {
		injected = true;
		return -EPIPE;
	}
	snd_pcm_sframes_t count = ((__typeof__(&snd_pcm_readi))dlsym(RTLD_NEXT,"snd_pcm_readi"))(pcm,buf,frames);
	static unsigned samples[2], crossings[2], maximum[2];
	static short previous[2];
	int stage = elapsed() >= 6.5 ? 1 : 0;
	if (count > 0 && samples[stage] < 48000) {
		const short *values = buf;
		for (snd_pcm_sframes_t i=0; i<count; i++) {
			short x = values[2*i];
			unsigned mag = x < 0 ? -(int)x : (int)x;
			if (mag > maximum[stage]) maximum[stage]=mag;
			if (x > 0 && previous[stage] <= 0) crossings[stage]++;
			previous[stage]=x;
		}
		samples[stage]+=(unsigned)count;
		if (samples[stage] >= 48000) {
			unsigned hz = crossings[stage]*48000/samples[stage];
			fprintf(stderr,"Q1 test ALSA PCM stage %d: %u Hz, peak=%u\n",stage,hz,maximum[stage]);
			if (stage && hz>=1470 && hz<=1550) flag("second-source-tone");
		}
	}
	return count;
}

int snd_pcm_recover(snd_pcm_t *pcm, int error, int silent)
{
	int rc = ((__typeof__(&snd_pcm_recover))dlsym(RTLD_NEXT,"snd_pcm_recover"))(pcm,error,silent);
	if (rc == 0 && injected && error == -EPIPE && snd_pcm_state(pcm) == SND_PCM_STATE_PREPARED) {
		recovered = true;
		flag("recovered-prepared");
		fprintf(stderr,"Q1 XRUN recovered to PREPARED\n");
	}
	return rc;
}

int snd_pcm_start(snd_pcm_t *pcm)
{
	snd_pcm_state_t before = snd_pcm_state(pcm);
	int rc = ((__typeof__(&snd_pcm_start))dlsym(RTLD_NEXT,"snd_pcm_start"))(pcm);
	if (recovered && rc == 0 && before == SND_PCM_STATE_PREPARED && snd_pcm_state(pcm) == SND_PCM_STATE_RUNNING) {
		flag("restarted-running");
		recovered=false;
		fprintf(stderr,"Q1 XRUN restarted to RUNNING\n");
	}
	return rc;
}
