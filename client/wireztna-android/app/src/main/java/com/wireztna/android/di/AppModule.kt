package com.wireztna.android.di

import android.content.Context
import com.wireztna.android.data.api.WireZtnaApi
import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.config.SecureKeyStore
import com.wireztna.android.tunnel.TunnelManager
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.android.qualifiers.ApplicationContext
import dagger.hilt.components.SingletonComponent
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.logging.HttpLoggingInterceptor
import retrofit2.Retrofit
import retrofit2.converter.kotlinx.serialization.asConverterFactory
import java.util.concurrent.TimeUnit
import javax.inject.Singleton

@Module
@InstallIn(SingletonComponent::class)
object AppModule {

    @Provides
    @Singleton
    fun provideJson(): Json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        encodeDefaults = true
    }

    @Provides
    @Singleton
    fun provideOkHttpClient(configStore: ConfigStore): OkHttpClient {
        return OkHttpClient.Builder()
            .connectTimeout(30, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .writeTimeout(30, TimeUnit.SECONDS)
            // Dynamic base URL interceptor: rewrites the placeholder host
            // with the real API URL from config (set after enrollment)
            .addInterceptor { chain ->
                val original = chain.request()
                val apiUrl = configStore.getApiUrlSync()
                val request = if (apiUrl != null && original.url.host == "localhost") {
                    val newUrl = original.url.toString().replace("http://localhost/", "$apiUrl/")
                    original.newBuilder().url(newUrl).build()
                } else {
                    original
                }
                chain.proceed(request)
            }
            // Auth token interceptor
            .addInterceptor { chain ->
                val request = chain.request()
                val token = configStore.getTokenSync()
                if (token != null && request.header("Authorization") == null) {
                    chain.proceed(
                        request.newBuilder()
                            .header("Authorization", "Bearer $token")
                            .build()
                    )
                } else {
                    chain.proceed(request)
                }
            }
            .addInterceptor(
                HttpLoggingInterceptor().apply {
                    level = HttpLoggingInterceptor.Level.BODY
                }
            )
            .build()
    }

    @Provides
    @Singleton
    fun provideRetrofit(client: OkHttpClient, json: Json, configStore: ConfigStore): Retrofit {
        val baseUrl = configStore.getApiUrlSync() ?: "http://localhost/"
        return Retrofit.Builder()
            .baseUrl(baseUrl)
            .client(client)
            .addConverterFactory(json.asConverterFactory("application/json".toMediaType()))
            .build()
    }

    @Provides
    @Singleton
    fun provideWireZtnaApi(retrofit: Retrofit): WireZtnaApi {
        return retrofit.create(WireZtnaApi::class.java)
    }

    @Provides
    @Singleton
    fun provideConfigStore(@ApplicationContext context: Context): ConfigStore {
        return ConfigStore(context)
    }

    @Provides
    @Singleton
    fun provideSecureKeyStore(@ApplicationContext context: Context): SecureKeyStore {
        return SecureKeyStore(context)
    }

    @Provides
    @Singleton
    fun provideTunnelManager(
        @ApplicationContext context: Context,
        configStore: ConfigStore,
        keyStore: SecureKeyStore,
    ): TunnelManager {
        return TunnelManager(context, configStore, keyStore)
    }
}
