{{- define "noroshi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "noroshi.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "noroshi.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "noroshi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "noroshi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "noroshi.labels" -}}
helm.sh/chart: {{ include "noroshi.chart" . }}
{{ include "noroshi.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "noroshi.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "noroshi.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "noroshi.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}
{{- end }}

{{- define "noroshi.configMapName" -}}
{{- default (include "noroshi.fullname" .) .Values.existingConfigMap }}
{{- end }}

{{/* Service name for a listener: public keeps the plain name. */}}
{{- define "noroshi.serviceName" -}}
{{- $root := index . 0 }}
{{- $listener := index . 1 }}
{{- if eq $listener "public" }}
{{- include "noroshi.fullname" $root }}
{{- else }}
{{- printf "%s-%s" (include "noroshi.fullname" $root) $listener | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
