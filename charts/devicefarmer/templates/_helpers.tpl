{{/*
Name helpers
*/}}
{{- define "devicefarmer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "devicefarmer.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "devicefarmer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Labels. Call with: (dict "root" . "component" "app")
*/}}
{{- define "devicefarmer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "devicefarmer.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "devicefarmer.labels" -}}
helm.sh/chart: {{ include "devicefarmer.chart" .root }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/part-of: devicefarmer
{{ include "devicefarmer.selectorLabels" . }}
{{- with .root.Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
Image references
*/}}
{{- define "devicefarmer.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/*
Service / host names
*/}}
{{- define "devicefarmer.rethinkdbHost" -}}{{ include "devicefarmer.fullname" . }}-rethinkdb{{- end -}}
{{- define "devicefarmer.triproxyAppHost" -}}{{ include "devicefarmer.fullname" . }}-triproxy-app{{- end -}}
{{- define "devicefarmer.triproxyDevHost" -}}{{ include "devicefarmer.fullname" . }}-triproxy-dev{{- end -}}

{{- define "devicefarmer.secretName" -}}
{{- if .Values.secret.existingSecret -}}{{ .Values.secret.existingSecret }}{{- else -}}{{ include "devicefarmer.fullname" . }}-secret{{- end -}}
{{- end -}}

{{/*
Public URLs (driven by the ingress host + TLS toggle)
*/}}
{{- define "devicefarmer.scheme" -}}{{- if .Values.ingress.tls.enabled -}}https{{- else -}}http{{- end -}}{{- end -}}
{{- define "devicefarmer.wsScheme" -}}{{- if .Values.ingress.tls.enabled -}}wss{{- else -}}ws{{- end -}}{{- end -}}
{{- define "devicefarmer.publicUrl" -}}{{- printf "%s://%s" (include "devicefarmer.scheme" .) .Values.ingress.host -}}{{- end -}}
{{- define "devicefarmer.wsUrl" -}}{{- printf "%s://%s" (include "devicefarmer.wsScheme" .) .Values.ingress.host -}}{{- end -}}

{{/*
Reusable env snippets
*/}}
{{- define "devicefarmer.rethinkdbEnv" -}}
- name: RETHINKDB_PORT_28015_TCP
  value: "tcp://{{ include "devicefarmer.rethinkdbHost" . }}:28015"
- name: RETHINKDB_ENV_AUTHKEY
  value: ""
{{- end -}}

{{- define "devicefarmer.secretEnv" -}}
- name: SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "devicefarmer.secretName" . }}
      key: secret
{{- end -}}

{{/*
Init container that blocks until RethinkDB accepts connections.
*/}}
{{- define "devicefarmer.waitForRethinkdb" -}}
- name: wait-rethinkdb
  image: {{ .Values.waitImage | quote }}
  command:
    - sh
    - -c
    - 'until nc -z {{ include "devicefarmer.rethinkdbHost" . }} 28015; do echo "waiting for rethinkdb..."; sleep 2; done'
{{- end -}}
