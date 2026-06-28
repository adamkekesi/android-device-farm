# Headless Android emulator image for the device farm, with Google Play.
#
# Unlike the AOSP/google_apis images, this uses a google_apis_playstore system
# image: it ships a current Google Play Services (GMS) and is Play-certified, so
# Google Sign-In and the Play Store work (the slim google_apis images ship a
# years-old GMS and fail with "version not supported").
#
# Contract expected by the operator's Device pod (see device_controller.go):
#   * the default CMD boots the emulator with adb listening on 127.0.0.1:5555,
#   * `adb` + a shell are present (the readiness probe runs `adb shell getprop`),
#   * runs privileged with /dev/kvm mounted (KVM acceleration).
FROM eclipse-temurin:17-jdk-jammy

# Override at build time for a different level/variant, e.g.:
#   --build-arg PLATFORM=android-35 --build-arg SYS_IMG="system-images;android-35;google_apis_playstore;x86_64"
ARG PLATFORM=android-36
ARG SYS_IMG=system-images;android-36;google_apis_playstore;x86_64
ARG CMDLINE_TOOLS=commandlinetools-linux-11076708_latest.zip
# Pin the emulator to 34.1.20 (build 11610631). The current emulator (36.x) ships a
# QEMU that segfaults under this host's KVM, while 34.1.20 + KVM runs API 36 fine
# (it's the "emulator-legacy" build that boots locally on the same machine).
ARG EMULATOR_BUILD=11610631

ENV ANDROID_HOME=/opt/sdk \
    ANDROID_SDK_ROOT=/opt/sdk \
    AVD_NAME=play \
    PATH=/opt/sdk/cmdline-tools/latest/bin:/opt/sdk/platform-tools:/opt/sdk/emulator:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Libraries the headless emulator + swiftshader renderer need on Ubuntu.
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl unzip ca-certificates \
      libpulse0 libnss3 libxcomposite1 libxcursor1 libxi6 libxtst6 libxdamage1 \
      libxrandr2 libxshmfence1 libxkbcommon0 libasound2 libglu1-mesa libgl1 \
    && rm -rf /var/lib/apt/lists/*

# Android command-line tools.
RUN mkdir -p /opt/sdk/cmdline-tools && cd /tmp \
    && curl -fsSL -o ct.zip "https://dl.google.com/android/repository/${CMDLINE_TOOLS}" \
    && unzip -q ct.zip -d /opt/sdk/cmdline-tools \
    && mv /opt/sdk/cmdline-tools/cmdline-tools /opt/sdk/cmdline-tools/latest \
    && rm ct.zip

# Install the SDK + Play Store system image, create the AVD, then replace the
# SDK's emulator with the pinned 34.1.20 build — all in one layer so the throwaway
# stock emulator doesn't end up in the image. (avdmanager refuses to run unless an
# "emulator" package is registered, hence install-then-replace.)
RUN yes | sdkmanager --licenses >/dev/null 2>&1 || true \
    && sdkmanager --install "platform-tools" "emulator" "platforms;${PLATFORM}" "${SYS_IMG}" >/dev/null \
    && echo "no" | avdmanager create avd -n "${AVD_NAME}" -k "${SYS_IMG}" --force \
    && cd /tmp \
    && curl -fsSL -o emu.zip "https://dl.google.com/android/repository/emulator-linux_x64-${EMULATOR_BUILD}.zip" \
    && rm -rf "${ANDROID_HOME}/emulator" \
    && unzip -q emu.zip -d "${ANDROID_HOME}" \
    && rm emu.zip

# Authorized adb keys, injected into the guest's adb_keys at boot. Belt-and-braces
# alongside ro.adb.secure=0: a throwaway farm key (used by the in-pod readiness
# probe) plus any host public keys appended at build time.
COPY adbkeys/adbkey /root/.android/adbkey
COPY adbkeys/adbkey.pub /root/.android/adbkey.pub
RUN chmod 600 /root/.android/adbkey

COPY start.sh /opt/sdk/start.sh
RUN chmod +x /opt/sdk/start.sh

EXPOSE 5555
CMD ["/bin/bash", "-c", "/opt/sdk/start.sh"]
