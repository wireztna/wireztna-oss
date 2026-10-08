# WireZTNA Android ProGuard Rules

# Keep WireGuard tunnel classes
-keep class com.wireguard.** { *; }

# Keep Kotlin serialization
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt
-keepclassmembers class kotlinx.serialization.json.** { *** Companion; }
-keepclasseswithmembers class kotlinx.serialization.json.** { kotlinx.serialization.KSerializer serializer(...); }
-keep,includedescriptorclasses class com.wireztna.android.**$$serializer { *; }
-keepclassmembers class com.wireztna.android.** { *** Companion; }
-keepclasseswithmembers class com.wireztna.android.** { kotlinx.serialization.KSerializer serializer(...); }

# Keep Retrofit interfaces
-keep,allowobfuscation interface com.wireztna.android.data.api.**

# OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**
